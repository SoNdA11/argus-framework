package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"argus-framework/pkg/ble"
	"argus-framework/pkg/config"

	blelib "github.com/go-ble/ble"
	"github.com/go-ble/ble/linux"
	"github.com/gorilla/websocket"
)

// --- Estruturas para comunicação WebSocket (sem alterações) ---
type AgentCommand struct {
	Action  string                 `json:"action"`
	Payload map[string]interface{} `json:"payload"`
}
type AgentEvent struct {
	Event   string                 `json:"event"`
	Payload map[string]interface{} `json:"payload"`
}

// --- Funções Auxiliares (sem alterações) ---

// discoverAdapters encontra 2 adaptadores
func discoverAdapters() (int, int, error) { /* ...código da versão anterior... */
	log.Println("[AGENT-DISCOVERY] Procurando por 2 adaptadores BLE disponíveis...")
	var availableIDs []int
	for i := 0; i < 10; i++ {
		d, err := linux.NewDevice(blelib.OptDeviceID(i))
		if err != nil { continue }
		if err := d.Stop(); err != nil { log.Printf("[AGENT-DISCOVERY] Aviso: falha ao fechar hci%d: %v", i, err) }
		log.Printf("[AGENT-DISCOVERY] ✅ Adaptador hci%d encontrado.", i)
		availableIDs = append(availableIDs, i)
		if len(availableIDs) == 2 { break }
	}
	if len(availableIDs) < 2 { return -1, -1, fmt.Errorf("falha: 2 adaptadores não encontrados") }
	clientID, serverID := availableIDs[0], availableIDs[1]
	log.Printf("[AGENT-DISCOVERY] Atribuição: hci%d (CLIENTE) | hci%d (SERVIDOR)", clientID, serverID)
	return clientID, serverID, nil
}

// writePump envia mensagens para o WebSocket
func writePump(ctx context.Context, c *websocket.Conn, writeChan <-chan interface{}, done chan struct{}) { /* ...código da versão anterior... */
	pingTicker := time.NewTicker(30 * time.Second)
	defer func() { pingTicker.Stop(); c.Close() }()
	for {
		select {
		case <-ctx.Done(): log.Println("[AGENT-WS] Encerrando write pump (sinal)..."); c.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")); return
		case <-done: log.Println("[AGENT-WS] Encerrando write pump (conexão perdida)..."); return
		case msg, ok := <-writeChan: if !ok { log.Println("[AGENT-WS] Canal fechado."); return }; if err := c.WriteJSON(msg); err != nil { log.Printf("[AGENT-WS] ❌ Erro escrita: %v", err); return }
		case <-pingTicker.C: if err := c.WriteMessage(websocket.PingMessage, nil); err != nil { log.Printf("[AGENT-WS] ❌ Erro ping: %v", err); return }
		}
	}
}

// Agora a leitura de uiState.MainMode funcionará.
func localServerRoutine(ctx context.Context, cfg *config.AppConfig, uiState *ble.UIState, powerChan <-chan int, cadenceChan <-chan int, writeChan chan<- interface{}, wg *sync.WaitGroup) {
	defer wg.Done()
	log.Printf("[AGENT-SRV] Iniciando rolo virtual no adaptador hci%d...", cfg.ServerAdapterID)
	d, err := linux.NewDevice(blelib.OptDeviceID(cfg.ServerAdapterID))
	if err != nil {
		log.Printf("[AGENT-SRV] ❌ Falha adaptador: %s", err)
		writeChan <- AgentEvent{"error", map[string]interface{}{"message": err.Error()}}
		return
	}

	powerSvc := blelib.NewService(ble.PowerSvcUUID)
	powerChar := powerSvc.NewCharacteristic(ble.PowerCharUUID)
	powerChar.HandleNotify(blelib.NotifyHandlerFunc(func(req blelib.Request, ntf blelib.Notifier) {
		log.Printf("[AGENT-SRV] ✅ App %s inscrito Potência.", req.Conn().RemoteAddr())
		select {
		case writeChan <- AgentEvent{"app_status", map[string]interface{}{"connected": true}}:
		default:
		}
		defer func() {
			log.Printf("[AGENT-SRV] 🔌 App %s desinscrito Potência.", req.Conn().RemoteAddr())
			select {
			case writeChan <- AgentEvent{"app_status", map[string]interface{}{"connected": false}}:
			default:
			}
		}()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ntf.Context().Done():
				return
			case watts := <-powerChan:
				pBytes := make([]byte, 4)
				binary.LittleEndian.PutUint16(pBytes[2:4], uint16(watts))
				if _, err := ntf.Write(pBytes); err != nil {
					log.Printf("[AGENT-SRV] Erro envio potência: %v", err)
					return
				}
			}
		}
	}))

	cscSvc := blelib.NewService(ble.CSCSvcUUID)
	cscChar := cscSvc.NewCharacteristic(ble.CSCMeasurementCharUUID)
	cscChar.HandleNotify(blelib.NotifyHandlerFunc(func(req blelib.Request, ntf blelib.Notifier) {
		log.Printf("[AGENT-SRV] ✅ App %s inscrito Cadência.", req.Conn().RemoteAddr())
		defer log.Printf("[AGENT-SRV] 🔌 App %s desinscrito Cadência.", req.Conn().RemoteAddr())
		var cumulativeRevolutions uint32
		var lastCrankEventTime uint16
		var timeOfNextRevolution time.Time
		var currentCadenceTarget int
		var lastBotTarget int

		for {
			uiState.RLock()
			currentMode := uiState.MainMode // Esta linha agora compila
			realCadence := uiState.RealCadence
			uiState.RUnlock()

			if currentMode == "boost" {
				if realCadence >= 0 {
					currentCadenceTarget = realCadence
				} else {
					currentCadenceTarget = 0
				}
			} else { // modo "bot"
				select {
				case botTarget := <-cadenceChan:
					currentCadenceTarget = botTarget
					lastBotTarget = botTarget
				default:
					currentCadenceTarget = lastBotTarget
				}
			}

			select {
			case <-ctx.Done():
				return
			case <-ntf.Context().Done():
				return
			case <-time.After(50 * time.Millisecond):
				if currentCadenceTarget <= 0 {
					timeOfNextRevolution = time.Time{}
					continue
				}
				if timeOfNextRevolution.IsZero() {
					timeOfNextRevolution = time.Now()
				}
				if time.Now().Before(timeOfNextRevolution) {
					continue
				}

				cumulativeRevolutions++
				nowNano := time.Now().UnixNano()
				lastCrankEventTime = uint16(nowNano / 1e6 * 1024 / 1000)

				flags := byte(0x02)
				buf := new(bytes.Buffer)
				binary.Write(buf, binary.LittleEndian, flags)
				binary.Write(buf, binary.LittleEndian, uint16(cumulativeRevolutions&0xFFFF))
				binary.Write(buf, binary.LittleEndian, lastCrankEventTime)

				if _, err := ntf.Write(buf.Bytes()); err != nil {
					log.Printf("[AGENT-SRV] Erro envio cadência: %v", err)
					return
				}
				// log.Printf("[CAD %s] Enviado: RPM=%d", currentMode, currentCadenceTarget)
			}
		}
	}))

	d.AddService(powerSvc)
	d.AddService(cscSvc)
	d.AddService(blelib.NewService(ble.FTMSSvcUUID))
	log.Printf("[AGENT-SRV] 📣 Anunciando como '%s'...", cfg.VirtualTrainerName)
	if err = d.AdvertiseNameAndServices(ctx, cfg.VirtualTrainerName, ble.PowerSvcUUID, ble.FTMSSvcUUID, ble.CSCSvcUUID); err != nil {
		log.Printf("[AGENT-SRV] Erro anunciar: %v", err)
	}
	log.Println("[AGENT-SRV] Anúncio parado.")
}




// --- NOVA: dataBridge ---
// Goroutine que lê o estado do cliente e envia para o WebSocket
func dataBridge(ctx context.Context, uiState *ble.UIState, writeChan chan<- interface{}) {
	ticker := time.NewTicker(1 * time.Second) // Envia a cada segundo
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("[AGENT-BRIDGE] Encerrando ponte de dados.")
			return
		case <-ticker.C:
			uiState.RLock()
			realPower := uiState.RealPower
			clientConnected := uiState.ClientConnected // Pega o status da conexão do cliente
			uiState.RUnlock()

			// Envia apenas se o cliente estiver conectado e houver dados
			if clientConnected && realPower >= 0 { // >= 0 para incluir o caso de 0 watts
				select {
				case writeChan <- AgentEvent{"trainer_data", map[string]interface{}{"real_power": realPower}}:
				default:
					log.Println("[AGENT-BRIDGE] Aviso: Canal escrita cheio, descartando trainer_data.")
				}
			}
		}
	}
}


func main() {
	agentKey := flag.String("key", "", "Chave API")
	trainerMAC := flag.String("mac", "", "MAC Rolo Real")
	flag.Parse()
	if *agentKey == "" {
		log.Fatal("❌ --key obrigatória")
	}
	addr := "wss://argus-remote-server.onrender.com/agent"
	clientAdapterID, serverAdapterID, err := discoverAdapters()
	if err != nil {
		log.Fatalf("❌ %v", err)
	}
	log.Printf("[AGENT] Iniciando... Cliente(hci%d) -> Servidor(hci%d)", clientAdapterID, serverAdapterID)
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt, syscall.SIGTERM)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-interrupt; log.Println("Encerrando..."); cancel() }()

	powerChan := make(chan int, 10)
	cadenceChan := make(chan int, 10)
	uiState := &ble.UIState{}

	for {
		if ctx.Err() != nil {
			log.Println("Contexto cancelado. Saindo."); return
		}
		log.Printf("[AGENT] Conectando a %s", addr)
		c, _, err := websocket.DefaultDialer.Dial(addr, nil)
		if err != nil {
			log.Println("❌ Falha WS:", err); time.Sleep(5 * time.Second); continue
		}
		log.Println("[AGENT] ✅ Conectado WS! Autenticando...")
		authMsg := map[string]string{"agent_key": *agentKey}
		if err := c.WriteJSON(authMsg); err != nil {
			log.Println("❌ Falha auth:", err); c.Close(); time.Sleep(5 * time.Second); continue
		}

		writeChan := make(chan interface{}, 10)
		done := make(chan struct{})
		bleCtx, bleCancel := context.WithCancel(ctx)
		go writePump(ctx, c, writeChan, done)

		go func() { // Goroutine de leitura WS e disparo BLE
			defer func() {
				bleCancel()
				close(done)
			}()
			bleStarted := false
			var bleWg sync.WaitGroup
			for {
				var cmd AgentCommand
				if err := c.ReadJSON(&cmd); err != nil {
					log.Println("🔌 Erro leitura WS:", err)
					bleWg.Wait()
					return
				}

				switch cmd.Action {
				case "start_virtual_trainer":
					if !bleStarted {
						if name, ok := cmd.Payload["name"].(string); ok {
							log.Println("[AGENT] Comando 'start_virtual_trainer' recebido.")
							bleCfg := &config.AppConfig{
								ClientAdapterID:    clientAdapterID,
								ServerAdapterID:    serverAdapterID,
								TrainerMAC:         *trainerMAC,
								VirtualTrainerName: name,
							}
							dummyCommandChan := make(chan []byte)

							bleWg.Add(3)

							log.Println("[AGENT] Iniciando Cliente BLE (pkg/ble)...")

							go ble.ClientRoutine(bleCtx, bleCfg, dummyCommandChan, uiState, nil, &bleWg) // readyChan não é mais necessário
							log.Println("[AGENT] Iniciando Servidor BLE local...")
							go localServerRoutine(bleCtx, bleCfg, uiState, powerChan, cadenceChan, writeChan, &bleWg)
							log.Println("[AGENT] Iniciando Ponte de Dados...")
							go dataBridge(bleCtx, uiState, writeChan)
							bleStarted = true
						}
					} else {
						log.Println("[AGENT] Aviso: Comando 'start_virtual_trainer' recebido, mas BLE já iniciado.")
					}
				case "send_power":
					if watts, ok := cmd.Payload["watts"].(float64); ok {
						select {
						case powerChan <- int(watts):
						default:
							log.Println("[AGENT] Aviso: powerChan cheio.")
						}
					}
				case "send_cadence":
					if rpm, ok := cmd.Payload["rpm"].(float64); ok {
						select {
						case cadenceChan <- int(rpm):
						default:
							log.Println("[AGENT] Aviso: cadenceChan cheio.")
						}
					}
				// +++ ADICIONADO: Recebe e atualiza o modo +++
				case "set_mode":
					if mode, ok := cmd.Payload["mode"].(string); ok {
						log.Printf("[AGENT] Modo recebido do servidor: %s", mode)
						uiState.Lock()
						uiState.MainMode = mode
						uiState.Unlock()
					}
				// +++ FIM DA ADIÇÃO +++
				}
			}
		}()

		select {
		case <-done:
			log.Println("[AGENT] Conexão WS perdida. Reconectando...")
		case <-ctx.Done():
			log.Println("Sinal recebido. Fechando..."); time.Sleep(1 * time.Second); return
		}
	}
}