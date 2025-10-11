// Local: cmd/remote-server/main.go
package main

import (
	"log"
	"net/http"
	"os"
	"time"
	"github.com/gorilla/websocket"
)

type AgentCommand struct {
	Action  string                 `json:"action"`
	Payload map[string]interface{} `json:"payload"`
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func handleConnections(w http.ResponseWriter, r *http.Request) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Erro no upgrade: %v", err)
		return
	}
	defer ws.Close()
	log.Println("[SERVER] ✅ Agente local conectado!")

	// --- MUDANÇA 1: Lançamos uma goroutine para enviar os comandos de potência ---
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()

		// Envia o comando inicial para o agente criar o rolo virtual.
		log.Println("[SERVER] >> Enviando comando 'start_virtual_trainer'...")
		startCmd := AgentCommand{
			Action:  "start_virtual_trainer",
			Payload: map[string]interface{}{"name": "Argus Cloud Trainer"},
		}
		if err := ws.WriteJSON(startCmd); err != nil {
			log.Printf("[SERVER] ❌ Erro ao enviar comando inicial: %v", err)
			return
		}

		// Loop para enviar dados de potência a cada segundo.
		for {
			select {
			case <-done: // Se a conexão for fechada, para o loop.
				return
			case <-ticker.C:
				powerCmd := AgentCommand{
					Action:  "send_power",
					Payload: map[string]interface{}{"watts": 150}, // Enviando 150W fixo por enquanto.
				}
				if err := ws.WriteJSON(powerCmd); err != nil {
					// Se houver erro, provavelmente o agente desconectou.
					return
				}
			}
		}
	}()
	// --- FIM DA MUDANÇA 1 ---

	// O loop de leitura agora apenas gerencia o fim da conexão.
	for {
		if _, _, err := ws.ReadMessage(); err != nil {
			log.Println("[SERVER] 🔌 Agente local desconectado:", err)
			close(done) // Sinaliza para a goroutine de envio parar.
			break
		}
	}
}

func main() {
	http.HandleFunc("/agent", handleConnections)
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Printf("🚀 Servidor Remoto iniciado na porta %s...", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatal("ListenAndServe: ", err)
	}
}