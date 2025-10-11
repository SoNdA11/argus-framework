// Local: cmd/local-agent/main.go
package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"log"
	"os"
	"os/signal"
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

var (
	PowerSvcUUID  = ble.MustParse("00001818-0000-1000-8000-00805f9b34fb")
	PowerCharUUID = ble.MustParse("00002a63-0000-1000-8000-00805f9b34fb")
	FTMSSvcUUID   = ble.MustParse("00001826-0000-1000-8000-00805f9b34fb")
)

// manageBLE agora gerencia todo o ciclo de vida do servidor BLE local.
func manageBLE(ctx context.Context, name string, adapterID int, powerChan <-chan int) {
	log.Printf("[AGENT-BLE] Iniciando rolo virtual no adaptador hci%d...", adapterID)
	d, err := linux.NewDevice(ble.OptDeviceID(adapterID))
	if err != nil {
		log.Printf("[AGENT-BLE] ❌ Falha ao selecionar adaptador: %s", err)
		return
	}
	ble.SetDefaultDevice(d)

	powerSvc := ble.NewService(PowerSvcUUID)
	powerChar := powerSvc.NewCharacteristic(PowerCharUUID)
	
	// O handler de potência agora é um consumidor do canal 'powerChan'.
	powerChar.HandleNotify(ble.NotifyHandlerFunc(func(req ble.Request, ntf ble.Notifier) {
		log.Printf("[AGENT-BLE] ✅ App %s inscrito para Potência.", req.Conn().RemoteAddr())
		defer log.Printf("[AGENT-BLE] 🔌 App %s desinscrito da Potência.", req.Conn().RemoteAddr())

		for {
			select {
			case <-ctx.Done(): // Se o programa principal for encerrado.
				return
			case <-ntf.Context().Done(): // Se o app se desconectar.
				return
			case watts := <-powerChan: // Se um novo valor de potência chegar do WebSocket.
				powerBytes := make([]byte, 4)
				binary.LittleEndian.PutUint16(powerBytes[2:4], uint16(watts))
				if _, err := ntf.Write(powerBytes); err != nil {
					log.Printf("[AGENT-BLE] Erro ao enviar notificação de potência: %v", err)
				}
			}
		}
	}))

	d.AddService(powerSvc)
	d.AddService(ble.NewService(FTMSSvcUUID)) // Mantemos o FTMS para compatibilidade de controle.
	
	log.Printf("[AGENT-BLE] 📣 Anunciando como '%s'...", name)
	err = ble.AdvertiseNameAndServices(ctx, name, PowerSvcUUID, FTMSSvcUUID)
	if err != nil && err != context.Canceled {
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

	// Canal para passar os valores de potência do WebSocket para a lógica BLE.
	powerChan := make(chan int)

	for {
		if ctx.Err() != nil { log.Println("Contexto cancelado. Saindo."); return }

		log.Printf("[AGENT] Tentando se conectar a %s", addr)
		c, _, err := websocket.DefaultDialer.Dial(addr, nil)
		if err != nil {
			log.Println("[AGENT] ❌ Falha ao conectar, tentando novamente em 5 segundos:", err)
			time.Sleep(5 * time.Second)
			continue
		}
		
		log.Println("[AGENT] ✅ Conectado ao Servidor Remoto!")
		
		// Loop para ler comandos do servidor.
		for {
			_, message, err := c.ReadMessage()
			if err != nil {
				log.Println("[AGENT] 🔌 Desconectado do servidor:", err)
				c.Close()
				break
			}

			var cmd AgentCommand
			if err := json.Unmarshal(message, &cmd); err != nil {
				log.Printf("[AGENT] Erro ao decodificar comando: %v", err)
				continue
			}

			switch cmd.Action {
			case "start_virtual_trainer":
				log.Printf("[AGENT] << Comando '%s' recebido!", cmd.Action)
				if name, ok := cmd.Payload["name"].(string); ok {
					go manageBLE(ctx, name, 0, powerChan)
				}
			case "send_power":
				if watts, ok := cmd.Payload["watts"].(float64); ok {
					// Envia o valor de potência para o canal, que será lido pela goroutine do BLE.
					powerChan <- int(watts)
				}
			default:
				log.Printf("[AGENT] << Comando desconhecido recebido: %s", cmd.Action)
			}
		}
		c.Close()
	}
}