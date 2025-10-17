// Local: cmd/local-agent/main.go
package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-ble/ble"
	"github.com/go-ble/ble/linux"
	"github.com/gorilla/websocket"
)

// AgentCommand define a estrutura de um comando recebido do servidor remoto.
type AgentCommand struct {
	Action  string                 `json:"action"`
	Payload map[string]interface{} `json:"payload"`
}

// AgentEvent define a estrutura de um evento que o agente envia PARA o servidor.
type AgentEvent struct {
	Event   string                 `json:"event"`
	Payload map[string]interface{} `json:"payload"`
}

// UUIDs essenciais para o rolo virtual.
var (
	PowerSvcUUID           = ble.MustParse("00001818-0000-1000-8000-00805f9b34fb")
	PowerCharUUID          = ble.MustParse("00002a63-0000-1000-8000-00805f9b34fb")
	CSCSvcUUID             = ble.MustParse("00001816-0000-1000-8000-00805f9b34fb")
	CSCMeasurementCharUUID = ble.MustParse("00002a5b-0000-1000-8000-00805f9b34fb")
	FTMSSvcUUID            = ble.MustParse("00001826-0000-1000-8000-00805f9b34fb")
)

// manageBLE gerencia o ciclo de vida do servidor BLE local.
func manageBLE(ctx context.Context, name string, adapterID int, powerChan <-chan int, cadenceChan <-chan int, ws *websocket.Conn) {
	log.Printf("[AGENT-BLE] Iniciando rolo virtual no adaptador hci%d...", adapterID)
	d, err := linux.NewDevice(ble.OptDeviceID(adapterID))
	if err != nil {
		log.Printf("[AGENT-BLE] ❌ Falha ao selecionar adaptador: %s", err)
		return
	}
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
			case <-ctx.Done(): // Ouve o cancelamento para se encerrar
				return
			case <-ntf.Context().Done():
				return
			case watts := <-powerChan:
				powerBytes := make([]byte, 4)
				binary.LittleEndian.PutUint16(powerBytes[2:4], uint16(watts))
				if _, err := ntf.Write(powerBytes); err != nil {
					log.Printf("[AGENT-BLE] Erro ao enviar notificação de potência: %v", err)
				}
			}
		}
	}))

	cscSvc := ble.NewService(CSCSvcUUID)
	cscChar := cscSvc.NewCharacteristic(CSCMeasurementCharUUID)
	cscChar.HandleNotify(ble.NotifyHandlerFunc(func(req ble.Request, ntf ble.Notifier) {
		log.Printf("[AGENT-BLE] ✅ App %s inscrito para Cadência.", req.Conn().RemoteAddr())
		defer log.Printf("[AGENT-BLE] 🔌 App %s desinscrito da Cadência.", req.Conn().RemoteAddr())
		var cumulativeRevolutions, lastCrankEventTime uint16
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		var cadenciaAlvo int
		for {
			select {
			case <-ctx.Done(): // Ouve o cancelamento para se encerrar
				return
			case <-ntf.Context().Done():
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
		// Apenas loga o erro, não encerra o programa
		log.Printf("[AGENT-BLE] Erro ao anunciar: %v", err)
	}
	log.Println("[AGENT-BLE] Anúncio parado.")
}

func main() {
	adapterID := flag.Int("adapter", 0, "ID do adaptador HCI que o agente usará (ex: 0)")
	flag.Parse()

	addr := "wss://argus-remote-server.onrender.com/agent"
	log.Printf("[AGENT] Iniciando agente local no adaptador hci%d...", *adapterID)

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt, syscall.SIGTERM)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-interrupt
		log.Println("Sinal de interrupção recebido, encerrando agente...")
		cancel()
	}()
	defer cancel()

	powerChan := make(chan int, 10)
	cadenceChan := make(chan int, 10)

	for {
		if ctx.Err() != nil {
			log.Println("Contexto cancelado. Saindo do loop de reconexão.")
			return
		}

		log.Printf("[AGENT] Tentando se conectar a %s", addr)
		c, _, err := websocket.DefaultDialer.Dial(addr, nil)
		if err != nil {
			log.Println("[AGENT] ❌ Falha ao conectar, tentando novamente em 5 segundos:", err)
			time.Sleep(5 * time.Second)
			continue
		}
		log.Println("[AGENT] ✅ Conectado ao Servidor Remoto!")

		done := make(chan struct{})
		// CORREÇÃO 2: Cria um contexto específico para a sessão BLE.
		bleCtx, bleCancel := context.WithCancel(ctx)

		// Goroutine para ler mensagens do servidor
		go func() {
			defer func() {
				bleCancel() // Se a leitura parar, cancela a goroutine do BLE.
				close(done)
			}()
			for {
				var cmd AgentCommand
				if err := c.ReadJSON(&cmd); err != nil {
					log.Println("[AGENT] 🔌 Erro de leitura (desconectado):", err)
					return
				}
				switch cmd.Action {
				case "start_virtual_trainer":
					if name, ok := cmd.Payload["name"].(string); ok {
						// Passa o 'bleCtx' para a goroutine do BLE.
						go manageBLE(bleCtx, name, *adapterID, powerChan, cadenceChan, c)
					}
				case "send_power":
					if watts, ok := cmd.Payload["watts"].(float64); ok {
						// CORREÇÃO 1: Envio não-bloqueante para o canal.
						select {
						case powerChan <- int(watts):
						default: // Se o canal estiver cheio, descarta o pacote e não bloqueia.
						}
					}
				case "send_cadence":
					if rpm, ok := cmd.Payload["rpm"].(float64); ok {
						// CORREÇÃO 1: Envio não-bloqueante para o canal.
						select {
						case cadenceChan <- int(rpm):
						default:
						}
					}
				}
			}
		}()

		// CORREÇÃO 3: Goroutine de Heartbeat para manter a conexão viva.
		go func() {
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					if err := c.WriteMessage(websocket.PingMessage, []byte{}); err != nil {
						log.Println("[AGENT] Erro ao enviar ping:", err)
						return
					}
				case <-done:
					return
				case <-ctx.Done():
					return
				}
			}
		}()

		select {
		case <-done:
			log.Println("[AGENT] Conexão perdida. Tentando reconectar...")
			c.Close()
			bleCancel() // Garante que a goroutine BLE anterior seja encerrada.
		case <-ctx.Done():
			log.Println("Sinal de encerramento recebido. Fechando conexão WebSocket...")
			c.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
			time.Sleep(1 * time.Second)
			return
		}
	}
}