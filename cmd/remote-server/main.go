// Local: cmd/remote-server/main.go

package main

import (
	"os"
	"log"
	"net/http"
	"github.com/gorilla/websocket"
)

// 'upgrader' atualiza conexões HTTP para WebSocket.
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// handleConnections gerencia uma conexão de um 'local-agent'.
func handleConnections(w http.ResponseWriter, r *http.Request) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Fatal(err)
	}
	defer ws.Close()

	log.Println("[SERVER] ✅ Agente local conectado!")

	// Loop para manter a conexão viva e, no futuro, ler mensagens do agente.
	for {
		_, _, err := ws.ReadMessage()
		if err != nil {
			log.Println("[SERVER] 🔌 Agente local desconectado:", err)
			break
		}
	}
}

func main() {
	// Define a rota onde o agente vai se conectar.
	http.HandleFunc("/agent", handleConnections)

	// Tenta obter a porta do ambiente, senão usa 8080 (padrão para nuvens como o Render).
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Printf("🚀 Servidor Remoto iniciado na porta %s. Aguardando conexão do agente...", port)
	err := http.ListenAndServe(":"+port, nil)
	if err != nil {
		log.Fatal("ListenAndServe: ", err)
	}
}