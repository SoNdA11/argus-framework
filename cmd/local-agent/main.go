package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"
	"flag"

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

func manageBLE(ctx context.Context, name string, adapterID int, powerChan <-chan int, cadenceChan <-chan int, ws *websocket.Conn) {
	log.Printf("[AGENT-BLE] Iniciando rolo virtual no adaptador hci%d...", adapterID)
	d, err := linux.NewDevice(ble.OptDeviceID(adapterID))
	if err != nil { log.Printf("[AGENT-BLE] ❌ Falha ao selecionar adaptador: %s", err); return }
	ble.SetDefaultDevice(d)

	powerSvc := ble.NewService(PowerSvcUUID)
	powerChar := powerSvc.NewCharacteristic(PowerCharUUID)
	powerChar.HandleNotify(ble.NotifyHandlerFunc(func(req ble.Request, ntf ble.Notifier) {
		log.Printf("[AGENT-BLE] ✅ App %s inscrito para Potência.", req.Conn().RemoteAddr())
		ws.WriteJSON(AgentEvent{"app_status", map[string]interface{}{"connected": true}})
		defer func() {
			log.Printf("[AGENT-BLE] 🔌 App %s desinscrito da Potência.", req.Conn().RemoteAddr())
			ws.WriteJSON(AgentEvent{"app_status", map[string]interface{}{"connected": false}})
		}()
		for {
			select {
			case <-ctx.Done(): return
			case <-ntf.Context().Done(): return
			case watts := <-powerChan:
				powerBytes := make([]byte, 4)
				binary.LittleEndian.PutUint16(powerBytes[2:4], uint16(watts))
				if _, err := ntf.Write(powerBytes); err != nil { log.Printf("[AGENT-BLE] Erro ao enviar potência: %v", err) }
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

		ticker := time.NewTicker(250 * time.Millisecond) // Ticker de 4x por segundo
		defer ticker.Stop()
		
		var cadenciaAlvo int

		for {
			select {
			case <-ctx.Done(): return
			case <-ntf.Context().Done(): return
			// Ouve por um novo alvo de cadência do servidor...
			case novoAlvo := <-cadenceChan:
				cadenciaAlvo = novoAlvo
			// ...e a cada tick, envia o pacote atualizado.
			case <-ticker.C:
				if cadenciaAlvo <= 0 { continue }
				
				revolutionsInInterval := float64(cadenciaAlvo) / 60.0 / 4.0 // Revs por 0.25s
				cumulativeRevolutions += uint16(revolutionsInInterval)
				lastCrankEventTime += (1024 / 4)

				flags := byte(0x02)
				buf := new(bytes.Buffer)
				binary.Write(buf, binary.LittleEndian, flags)
				binary.Write(buf, binary.LittleEndian, cumulativeRevolutions)
				binary.Write(buf, binary.LittleEndian, lastCrankEventTime)
				if _, err := ntf.Write(buf.Bytes()); err != nil { return }
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

func main() {
    // 1. Define a nova flag para o adaptador
    adapterID := flag.Int("adapter", 0, "ID do adaptador HCI que o agente usará para o rolo virtual (ex: 0 para hci0)")
    flag.Parse() // Faz a leitura da flag

	addr := "wss://argus-remote-server.onrender.com/agent"
	log.Printf("[AGENT] Iniciando agente local no adaptador hci%d...", *adapterID)

	// Cria um canal para ouvir por sinais de interrupção (Ctrl+C).
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt, syscall.SIGTERM)

	// Cria o contexto principal que pode ser cancelado para encerrar todas as goroutines.
	ctx, cancel := context.WithCancel(context.Background())
	// Garante que 'cancel()' seja chamado ao sair da função main, limpando tudo.
	defer cancel()

	// Inicia uma goroutine para esperar pelo Ctrl+C e cancelar o contexto.
	go func() {
		<-interrupt
		log.Println("Sinal de interrupção recebido, encerrando agente...")
		cancel()
	}()

	// Canais para comunicação entre a lógica de WebSocket e a de BLE.
	powerChan := make(chan int, 10)
	cadenceChan := make(chan int, 10)

	// Loop infinito de reconexão.
	for {
		// Se o contexto foi cancelado (Ctrl+C), sai do loop e encerra o programa.
		if ctx.Err() != nil {
			log.Println("Contexto cancelado. Saindo do loop de reconexão.")
			return
		}

		log.Printf("[AGENT] Tentando se conectar a %s", addr)
		c, _, err := websocket.DefaultDialer.Dial(addr, nil)
		if err != nil {
			log.Println("[AGENT] ❌ Falha ao conectar, tentando novamente em 5 segundos:", err)
			time.Sleep(5 * time.Second)
			continue // Tenta a conexão novamente.
		}
		log.Println("[AGENT] ✅ Conectado ao Servidor Remoto!")

		// 'done' é um canal para sinalizar que a conexão foi perdida.
		done := make(chan struct{})
		
		// Inicia uma goroutine dedicada APENAS para ler mensagens do servidor.
		go func() {
			defer close(done) // Sinaliza que a leitura terminou (conexão caiu).
			for {
				var cmd AgentCommand
				if err := c.ReadJSON(&cmd); err != nil {
					log.Println("[AGENT] 🔌 Erro de leitura (desconectado):", err)
					return // Encerra esta goroutine de leitura.
				}

				// Processa os comandos recebidos do servidor.
				switch cmd.Action {
				case "start_virtual_trainer":
					log.Printf("[AGENT] << Comando '%s' recebido!", cmd.Action)
					if name, ok := cmd.Payload["name"].(string); ok {
						// CORREÇÃO APLICADA AQUI:
						// Usa o valor da flag (*adapterID) em vez de '0'.
						go manageBLE(ctx, name, *adapterID, powerChan, cadenceChan, c)
					}
				case "send_power":
					if watts, ok := cmd.Payload["watts"].(float64); ok {
						powerChan <- int(watts)
					}
				case "send_cadence":
					if rpm, ok := cmd.Payload["rpm"].(float64); ok {
						select {
						case cadenceChan <- int(rpm):
						default:
							// O canal de cadência também está cheio. Descartar.
						}
					}
				default:
					log.Printf("[AGENT] << Comando desconhecido: %s", cmd.Action)
				}
			}
		}()

		// O loop principal agora fica aqui, esperando por uma desconexão OU pelo Ctrl+C.
		select {
		case <-done:
			// 'done' foi fechado pela goroutine de leitura, indicando desconexão.
			log.Println("[AGENT] Conexão perdida. Tentando reconectar...")
			c.Close() // Fecha a conexão antiga antes de tentar uma nova.
		case <-ctx.Done():
			// O usuário apertou Ctrl+C.
			log.Println("Sinal de encerramento recebido. Fechando conexão WebSocket...")
			// Envia uma mensagem de fechamento limpo para o servidor.
			c.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
			time.Sleep(1 * time.Second) // Dá um tempo para a mensagem ser enviada.
			return                      // Encerra a função main e o programa.
		}
	}
}