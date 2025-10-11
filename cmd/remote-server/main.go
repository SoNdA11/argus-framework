// Local: cmd/remote-server/main.go

package main

import (
	"log"
	"net/http"
	"os" 
	"github.com/gorilla/websocket"
)

// Estrutura para os nossos comandos.
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

	// --- MUDANÇA AQUI ---
	// Assim que o agente se conecta, envia o comando para iniciar o rolo virtual.
	log.Println("[SERVER] >> Enviando comando 'start_virtual_trainer' para o agente...")
	command := AgentCommand{
		Action: "start_virtual_trainer",
		Payload: map[string]interface{}{
			"name": "Argus Cloud Trainer", // Um nome diferente para sabermos que é a nova versão
		},
	}
	if err := ws.WriteJSON(command); err != nil {
		log.Printf("[SERVER] ❌ Erro ao enviar comando inicial: %v", err)
		return
	}
	// --- FIM DA MUDANÇA ---

	for {
		_, _, err := ws.ReadMessage()
		if err != nil {
			log.Println("[SERVER] 🔌 Agente local desconectado:", err)
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