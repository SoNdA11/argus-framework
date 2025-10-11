// Local: cmd/local-agent/main.go

package main

import (
	"context"
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

// Estrutura para receber os comandos do servidor.
type AgentCommand struct {
	Action  string                 `json:"action"`
	Payload map[string]interface{} `json:"payload"`
}

// UUIDs Mínimos para o rolo virtual funcionar.
var (
	PowerSvcUUID = ble.MustParse("00001818-0000-1000-8000-00805f9b34fb")
	FTMSSvcUUID  = ble.MustParse("00001826-0000-1000-8000-00805f9b34fb")
)

// startAdvertising inicia o rolo virtual.
func startAdvertising(ctx context.Context, name string, adapterID int) {
	log.Printf("[AGENT-BLE] Iniciando rolo virtual no adaptador hci%d...", adapterID)
	d, err := linux.NewDevice(ble.OptDeviceID(adapterID))
	if err != nil {
		log.Printf("[AGENT-BLE] ❌ Falha ao selecionar adaptador: %s", err)
		return
	}
	ble.SetDefaultDevice(d)

	// Adiciona os serviços essenciais (sem handlers por enquanto, apenas para anunciar).
	d.AddService(ble.NewService(PowerSvcUUID))
	d.AddService(ble.NewService(FTMSSvcUUID))
	
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

	// Goroutine para encerrar o contexto quando Ctrl+C for pressionado.
	go func() {
		<-interrupt
		log.Println("Encerrando agente...")
		cancel()
	}()

	for {
		// Verifica se o programa foi encerrado antes de tentar reconectar.
		if ctx.Err() != nil {
			log.Println("Contexto cancelado. Saindo.")
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

		// Loop para ler comandos do servidor.
		for {
			_, message, err := c.ReadMessage()
			if err != nil {
				log.Println("[AGENT] 🔌 Desconectado do servidor:", err)
				c.Close()
				break // Sai do loop interno para tentar reconectar.
			}

			var cmd AgentCommand
			if err := json.Unmarshal(message, &cmd); err != nil {
				log.Printf("[AGENT] Erro ao decodificar comando: %v", err)
				continue
			}

			// Processa o comando recebido.
			switch cmd.Action {
			case "start_virtual_trainer":
				log.Printf("[AGENT] << Comando '%s' recebido!", cmd.Action)
				if name, ok := cmd.Payload["name"].(string); ok {
					// Inicia o anúncio BLE em uma nova goroutine para não travar a conexão WebSocket.
					// Usamos o adaptador 0 por padrão. Mude se necessário.
					go startAdvertising(ctx, name, 0) 
				}
			default:
				log.Printf("[AGENT] << Comando desconhecido recebido: %s", cmd.Action)
			}
		}
		c.Close()
	}
}