// Local: cmd/local-agent/main.go
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

// manageBLE gerencia todo o ciclo de vida do servidor BLE local.
func manageBLE(ctx context.Context, name string, adapterID int, powerChan <-chan int, cadenceChan <-chan int, ws *websocket.Conn) {
	log.Printf("[AGENT-BLE] Iniciando rolo virtual no adaptador hci%d...", adapterID)
	d, err := linux.NewDevice(ble.OptDeviceID(adapterID))
	if err != nil {
		log.Printf("[AGENT-BLE] ❌ Falha ao selecionar adaptador: %s", err)
		return
	}
	ble.SetDefaultDevice(d)

	// --- Serviço de Potência ---
	powerSvc := ble.NewService(PowerSvcUUID)
	powerChar := powerSvc.NewCharacteristic(PowerCharUUID)
	powerChar.HandleNotify(ble.NotifyHandlerFunc(func(req ble.Request, ntf ble.Notifier) {
		log.Printf("[AGENT-BLE] ✅ App %s inscrito para Potência.", req.Conn().RemoteAddr())
		// AVISA O SERVIDOR REMOTO QUE O APP CONECTOU
		ws.WriteJSON(AgentEvent{"app_status", map[string]interface{}{"connected": true}})
		
		defer func() {
			log.Printf("[AGENT-BLE] 🔌 App %s desinscrito da Potência.", req.Conn().RemoteAddr())
			// AVISA O SERVIDOR REMOTO QUE O APP DESCONECTOU
			ws.WriteJSON(AgentEvent{"app_status", map[string]interface{}{"connected": false}})
		}()

		for {
			select {
			case <-ctx.Done(): return
			case <-ntf.Context().Done(): return
			case watts := <-powerChan: // Se um novo valor de potência chegar...
				powerBytes := make([]byte, 4)
				binary.LittleEndian.PutUint16(powerBytes[2:4], uint16(watts))
				if _, err := ntf.Write(powerBytes); err != nil { // ...envia para o app.
					log.Printf("[AGENT-BLE] Erro ao enviar notificação de potência: %v", err)
				}
			}
		}
	}))

	// --- Serviço de Cadência ---
	cscSvc := ble.NewService(CSCSvcUUID)
	cscChar := cscSvc.NewCharacteristic(CSCMeasurementCharUUID)
	cscChar.HandleNotify(ble.NotifyHandlerFunc(func(req ble.Request, ntf ble.Notifier) {
		log.Printf("[AGENT-BLE] ✅ App %s inscrito para Cadência.", req.Conn().RemoteAddr())
		defer log.Printf("[AGENT-BLE] 🔌 %s App desinscrito da Cadência.", req.Conn().RemoteAddr())
		
		var cumulativeRevolutions, lastCrankEventTime uint16
		var timeOfNextRevolution time.Time

		for {
			select {
			case <-ctx.Done(): return
			case <-ntf.Context().Done(): return
			case cadenciaAlvo := <-cadenceChan: // Se um novo alvo de cadência chegar...
				if cadenciaAlvo <= 0 { timeOfNextRevolution = time.Time{}; continue }
				if timeOfNextRevolution.IsZero() { timeOfNextRevolution = time.Now() }
				
				// Simula o evento da pedalada para enviar o pacote.
				if time.Now().After(timeOfNextRevolution) {
					cumulativeRevolutions++
					lastCrankEventTime = uint16(time.Now().UnixNano() / 1e6 * 1024 / 1000)
					flags := byte(0x02)
					buf := new(bytes.Buffer)
					binary.Write(buf, binary.LittleEndian, flags)
					binary.Write(buf, binary.LittleEndian, cumulativeRevolutions)
					binary.Write(buf, binary.LittleEndian, lastCrankEventTime)
					if _, err := ntf.Write(buf.Bytes()); err != nil { return }
					
					intervalSeconds := 60.0 / float64(cadenciaAlvo)
					timeOfNextRevolution = time.Now().Add(time.Duration(intervalSeconds * float64(time.Second)))
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

func main() {
	addr := "wss://argus-remote-server.onrender.com/agent"
	log.Printf("[AGENT] Iniciando agente local...")

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt, syscall.SIGTERM)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { <-interrupt; log.Println("Encerrando agente..."); cancel() }()

	powerChan := make(chan int)
	cadenceChan := make(chan int)

	for {
		if ctx.Err() != nil { log.Println("Contexto cancelado. Saindo."); return }

		log.Printf("[AGENT] Tentando se conectar a %s", addr)
		c, _, err := websocket.DefaultDialer.Dial(addr, nil)
		if err != nil { log.Println("[AGENT] ❌ Falha...:", err); time.Sleep(5 * time.Second); continue }
		log.Println("[AGENT] ✅ Conectado ao Servidor Remoto!")

		for {
			var cmd AgentCommand
			if err := c.ReadJSON(&cmd); err != nil {
				log.Println("[AGENT] 🔌 Desconectado:", err); c.Close(); break
			}
			
			switch cmd.Action {
			case "start_virtual_trainer":
				log.Printf("[AGENT] << Comando '%s' recebido!", cmd.Action)
				if name, ok := cmd.Payload["name"].(string); ok {
					go manageBLE(ctx, name, 0, powerChan, cadenceChan, c)
				}
			case "send_power":
				if watts, ok := cmd.Payload["watts"].(float64); ok {
					powerChan <- int(watts)
				}
			case "send_cadence":
				if rpm, ok := cmd.Payload["rpm"].(float64); ok {
					cadenceChan <- int(rpm)
				}
			default:
				log.Printf("[AGENT] << Comando desconhecido: %s", cmd.Action)
			}
		}
		c.Close()
	}
}