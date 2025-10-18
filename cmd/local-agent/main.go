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
	"strings"    
	"syscall"   
	"time"

	"github.com/go-ble/ble"
	"github.com/go-ble/ble/linux"
	"github.com/gorilla/websocket"
)

type AgentCommand struct {
	Action  string                 `json:"action"`
	Payload map[string]interface{} `json:"payload"`
}
type AgentEvent struct {
	Event   string                 `json:"event"`
	Payload map[string]interface{} `json:"payload"`
}

var (
	PowerSvcUUID           = ble.MustParse("00001818-0000-1000-8000-00805f9b34fb")
	PowerCharUUID          = ble.MustParse("00002a63-0000-1000-8000-00805f9b34fb")
	CSCSvcUUID             = ble.MustParse("00001816-0000-1000-8000-00805f9b34fb")
	CSCMeasurementCharUUID = ble.MustParse("00002a5b-0000-1000-8000-00805f9b34fb")
	FTMSSvcUUID            = ble.MustParse("00001826-0000-1000-8000-00805f9b34fb")
)

// discoverAdapter procura por UM adaptador BLE funcional no sistema.
func discoverAdapter() (int, error) {
	log.Println("[AGENT-DISCOVERY] Procurando por adaptador BLE disponível...")

	// Tenta de hci0 a hci9
	for i := 0; i < 10; i++ {
		// Tenta inicializar o dispositivo
		d, err := linux.NewDevice(ble.OptDeviceID(i))
		if err != nil {
			// Se falhar (ex: "no such device" ou "RF-kill"), ignora e continua
			continue
		}

		// Se funcionou, fecha/para o dispositivo para liberar o recurso
		if err := d.Stop(); err != nil {
			log.Printf("[AGENT-DISCOVERY] Aviso: falha ao parar hci%d após teste: %v", i, err)
		}
		
		log.Printf("[AGENT-DISCOVERY] ✅ Adaptador hci%d encontrado e disponível.", i)
		return i, nil // Retorna o ID do primeiro que encontrar
	}

	// Se o loop terminar, nenhum foi encontrado
	return -1, fmt.Errorf("falha na descoberta: nenhum adaptador BLE disponível foi encontrado (verifique conexões e RF-kill)")
}

// Esta função será a ÚNICA goroutine autorizada a escrever na conexão
// para evitar o 'panic' de concorrência.
func writePump(ctx context.Context, c *websocket.Conn, writeChan <-chan interface{}, done chan struct{}) {
	pingTicker := time.NewTicker(30 * time.Second)
	defer func() {
		pingTicker.Stop()
		c.Close()
	}()

	for {
		select {
		case <-ctx.Done():
			// Contexto principal (Ctrl+C) foi cancelado
			log.Println("[AGENT-WS] Encerrando write pump (sinal de interrupção)...")
			// Envia uma mensagem de fechamento limpo
			c.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
			return
		case <-done:
			// 'done' é fechado se a goroutine de leitura (readPump) morrer.
			log.Println("[AGENT-WS] Encerrando write pump (conexão perdida)...")
			return
		case msg := <-writeChan:
			// Recebe uma mensagem do canal e a escreve no websocket
			if err := c.WriteJSON(msg); err != nil {
				log.Printf("[AGENT-WS] ❌ Erro ao escrever no websocket: %v", err)
				return // Encerra o pump se a escrita falhar
			}
		case <-pingTicker.C:
			// Envia o ping para manter a conexão viva
			if err := c.WriteMessage(websocket.PingMessage, nil); err != nil {
				log.Printf("[AGENT-WS] ❌ Erro ao enviar ping: %v", err)
				return // Encerra se o ping falhar
			}
		}
	}
}

// --- MODIFICADO: A função agora recebe o 'writeChan' para enviar dados
func manageBLE(ctx context.Context, name string, adapterID int, powerChan <-chan int, cadenceChan <-chan int, writeChan chan<- interface{}) {
	log.Printf("[AGENT-BLE] Iniciando rolo virtual no adaptador hci%d...", adapterID)
	d, err := linux.NewDevice(ble.OptDeviceID(adapterID))
	if err != nil {
		log.Printf("[AGENT-BLE] ❌ Falha ao selecionar adaptador: %s", err)
		// --- MODIFICADO: Envia para o canal, não escreve diretamente
		writeChan <- AgentEvent{"error", map[string]interface{}{"message": err.Error()}}
		return
	}
	ble.SetDefaultDevice(d)

	powerSvc := ble.NewService(PowerSvcUUID)
	powerChar := powerSvc.NewCharacteristic(PowerCharUUID)
	powerChar.HandleNotify(ble.NotifyHandlerFunc(func(req ble.Request, ntf ble.Notifier) {
		log.Printf("[AGENT-BLE] ✅ App %s inscrito para Potência.", req.Conn().RemoteAddr())
		// --- MODIFICADO: Envia para o canal, não escreve diretamente
		writeChan <- AgentEvent{"app_status", map[string]interface{}{"connected": true}}
		defer func() {
			log.Printf("[AGENT-BLE] 🔌 App %s desinscrito da Potência.", req.Conn().RemoteAddr())
			// --- MODIFICADO: Envia para o canal, não escreve diretamente
			// Nota: Esta é a linha que causava o 'panic' quando o ^C era pressionado.
			writeChan <- AgentEvent{"app_status", map[string]interface{}{"connected": false}}
		}()
		for {
			select {
			case <-ctx.Done():
				return
			// --- CORREÇÃO AQUI ---
			case <-ntf.Context().Done(): // Era ntf.Context.Done()
				return
			// --- FIM DA CORREÇÃO ---
			case watts := <-powerChan:
				powerBytes := make([]byte, 4)
				binary.LittleEndian.PutUint16(powerBytes[2:4], uint16(watts))
				if _, err := ntf.Write(powerBytes); err != nil {
					log.Printf("[AGENT-BLE] Erro ao enviar potência: %v", err)
				}
			}
		}
	}))

	cscSvc := ble.NewService(CSCSvcUUID)
	cscChar := cscSvc.NewCharacteristic(CSCMeasurementCharUUID)
	cscChar.HandleNotify(ble.NotifyHandlerFunc(func(req ble.Request, ntf ble.Notifier) {
		log.Printf("[AGENT-BLE] ✅ App %s inscrito para Cadência.", req.Conn().RemoteAddr())
		defer log.Printf("[AGENT-BLE] 🔌 App %s desinscrito da Cadência.", req.Conn().RemoteAddr())

		var cumulativeRevolutions uint16
		var lastCrankEventTime uint16

		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()

		var cadenciaAlvo int

		for {
			select {
			case <-ctx.Done():
				return
			case <-ntf.Context().Done(): // Esta já estava correta
				return
			case novoAlvo := <-cadenceChan:
				cadenciaAlvo = novoAlvo
			case <-ticker.C:
				if cadenciaAlvo <= 0 {
					continue
				}

				revolutionsInInterval := float64(cadenciaAlvo) / 60.0 / 4.0
				cumulativeRevolutions += uint16(revolutionsInInterval)
				lastCrankEventTime += (1024 / 4)

				flags := byte(0x02)
				buf := new(bytes.Buffer)
				binary.Write(buf, binary.LittleEndian, flags)
				binary.Write(buf, binary.LittleEndian, cumulativeRevolutions)
				binary.Write(buf, binary.LittleEndian, lastCrankEventTime)
				if _, err := ntf.Write(buf.Bytes()); err != nil {
					return
				}
			}
		}
	}))

	d.AddService(powerSvc)
	d.AddService(cscSvc)
	d.AddService(ble.NewService(FTMSSvcUUID))

	log.Printf("[AGENT-BLE] 📣 Anunciando como '%s'...", name)
	if err = ble.AdvertiseNameAndServices(ctx, name, PowerSvcUUID, FTMSSvcUUID, CSCSvcUUID); err != nil {
		log.Printf("[AGENT-BLE] Erro ao anunciar: %v", err)
	}
	log.Println("[AGENT-BLE] Anúncio parado.")
}

// Esta função será responsável por se conectar ao seu rolo de treino.
func manageTrainerConnection(ctx context.Context, mac string, adapterID int, writeChan chan<- interface{}) {
	if mac == "" {
		log.Println("[AGENT-TRAINER] ⚠️ MAC do rolo não fornecido (--mac). Apenas o rolo virtual funcionará.")
		return
	}

	log.Printf("[AGENT-TRAINER] Iniciando conexão com o rolo real (%s) via hci%d...", mac, adapterID)

	// Seleciona o mesmo adaptador. A biblioteca 'ble' lida com
	// múltiplos papéis (Central e Periférico) no mesmo adaptador.
	d, err := linux.NewDevice(ble.OptDeviceID(adapterID))
	if err != nil {
		log.Printf("[AGENT-TRAINER] ❌ Falha ao selecionar adaptador: %s", err)
		return
	}
	ble.SetDefaultDevice(d)

	// Loop de reconexão
	for {
		select {
		case <-ctx.Done():
			log.Println("[AGENT-TRAINER] Encerrando conexão com o rolo.")
			return
		default:
			// Tenta conectar
			log.Printf("[AGENT-TRAINER] 📡 Procurando por %s...", mac)
			client, err := ble.Connect(ctx, func(a ble.Advertisement) bool {
				// A função 'strings.EqualFold' agora irá compilar
				return strings.EqualFold(a.Addr().String(), mac)
			})
			if err != nil {
				log.Printf("[AGENT-TRAINER] Falha ao conectar: %v. Tentando novamente em 5s.", err)
				time.Sleep(5 * time.Second)
				continue
			}

			log.Println("[AGENT-TRAINER] ✅ Conectado ao rolo real!")

			// Descobre o perfil
			p, err := client.DiscoverProfile(true)
			if err != nil {
				log.Printf("[AGENT-TRAINER] ❌ Falha ao descobrir perfil: %v", err)
				client.CancelConnection()
				continue
			}

			// Procura a característica de potência
			powerChar := findCharacteristic(p, PowerCharUUID)
			if powerChar == nil {
				log.Println("[AGENT-TRAINER] ❌ Característica de potência (2A63) não encontrada no rolo real.")
				client.CancelConnection()
				continue
			}

			// (Opcional: Adicionar Cadência aqui se o rolo real a suportar)

			log.Println("[AGENT-TRAINER] 🔔 Inscrevendo-se para dados de potência real...")
			if err := client.Subscribe(powerChar, false, func(data []byte) {
				// (Fase 2) Envia dados para o servidor
				if len(data) >= 4 {
					powerValue := binary.LittleEndian.Uint16(data[2:4])
					// Envia o dado para o servidor via o write pump
					writeChan <- AgentEvent{"trainer_data", map[string]interface{}{
						"real_power": int(powerValue),
					}}
				}
			}); err != nil {
				log.Printf("[AGENT-TRAINER] ❌ Falha ao se inscrever: %v", err)
				client.CancelConnection()
				continue
			}

			// Espera pela desconexão
			<-client.Disconnected()
			log.Println("[AGENT-TRAINER] 🔌 Desconectado do rolo real. Tentando reconectar...")
		}
	}
}
// (Fase 1) Função auxiliar para encontrar características
func findCharacteristic(p *ble.Profile, uuid ble.UUID) *ble.Characteristic {
	for _, s := range p.Services {
		for _, c := range s.Characteristics {
			if c.UUID.Equal(uuid) {
				return c
			}
		}
	}
	return nil
}

func main() {
	adapterFlag := flag.Int("adapter", -1, "ID do adaptador HCI (ex: 0). Padrão -1 para auto-descoberta.")
	agentKey := flag.String("key", "", "Chave de Agente (API Key) para autenticação")
	// (Fase 1) Flag para o MAC do rolo
	trainerMAC := flag.String("mac", "", "MAC Address do rolo de treino real (ex: AA:BB:CC:11:22:33)")
	flag.Parse()

	if *agentKey == "" {
		log.Fatal("❌ Erro: A flag --key é obrigatória. Obtenha a chave no seu dashboard.")
	}

	addr := "wss://argus-remote-server.onrender.com/agent"

	var finalAdapterID int
	if *adapterFlag == -1 {
		id, err := discoverAdapter()
		if err != nil {
			log.Fatalf("❌ %v", err)
		}
		finalAdapterID = id
	} else {
		log.Printf("[AGENT] Usando adaptador manual hci%d conforme flag.", *adapterFlag)
		finalAdapterID = *adapterFlag
	}

	log.Printf("[AGENT] Iniciando agente local no adaptador hci%d...", finalAdapterID)

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt, syscall.SIGTERM)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-interrupt; log.Println("Encerrando agente..."); cancel() }()

	powerChan := make(chan int, 10)
	cadenceChan := make(chan int, 10)

	for {
		if ctx.Err() != nil {
			log.Println("Contexto cancelado. Saindo."); return
		}

		log.Printf("[AGENT] Tentando se conectar a %s", addr)
		c, _, err := websocket.DefaultDialer.Dial(addr, nil)
		if err != nil {
			log.Println("❌ Falha...:", err); time.Sleep(5 * time.Second); continue
		}

		log.Println("[AGENT] ✅ Conectado! Autenticando com a Chave de Agente...")
		authMsg := map[string]string{"agent_key": *agentKey}
		
		// NOTA: O writePump ainda não está rodando, então é seguro
		// fazer esta primeira escrita de autenticação diretamente.
		if err := c.WriteJSON(authMsg); err != nil {
			log.Println("❌ Falha ao enviar chave de autenticação:", err)
			c.Close(); time.Sleep(5 * time.Second); continue
		}

		// Canal de escrita para este loop de conexão
		writeChan := make(chan interface{}, 10)
		done := make(chan struct{})
		bleCtx, bleCancel := context.WithCancel(ctx)

		// Inicia o writePump para esta conexão
		go writePump(ctx, c, writeChan, done)

		go func() {
			defer func() { bleCancel(); close(done) }() // bleCancel() é importante aqui
			for {
				var cmd AgentCommand
				if err := c.ReadJSON(&cmd); err != nil {
					log.Println("🔌 Erro de leitura:", err); return
				}

				switch cmd.Action {
				case "start_virtual_trainer":
					if name, ok := cmd.Payload["name"].(string); ok {
						// Passa o 'writeChan' para o manageBLE
						go manageBLE(bleCtx, name, finalAdapterID, powerChan, cadenceChan, writeChan)

						// (Fase 1) Inicia a conexão com o rolo real
						go manageTrainerConnection(bleCtx, *trainerMAC, finalAdapterID, writeChan)
					}
				case "send_power":
					if watts, ok := cmd.Payload["watts"].(float64); ok {
						select { case powerChan <- int(watts): default: }
					}
				case "send_cadence":
					if rpm, ok := cmd.Payload["rpm"].(float64); ok {
						select { case cadenceChan <- int(rpm): default: }
					}
				}
			}
		}()

		select {
		case <-done:
			log.Println("[AGENT] Conexão perdida. Tentando reconectar...")
			// O 'defer' na goroutine acima já chama bleCancel()
			// O 'defer' no writePump já fecha o 'c.Close()'
		case <-ctx.Done():
			log.Println("Sinal de encerramento recebido. Fechando conexão...")
			// O 'writePump' vai detectar o ctx.Done() e fechar a conexão
			time.Sleep(1 * time.Second)
			return
		}
	}
}