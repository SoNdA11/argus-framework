// Local: cmd/remote-server/main.go

package main

import (
	"log"
	"net/http"
	"os" 
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func handleConnections(w http.ResponseWriter, r *http.Request) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Erro no upgrade do websocket: %v", err)
		return
	}
	defer ws.Close()

	log.Println("[SERVER] ✅ Agente local conectado!")

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

	log.Printf("🚀 Servidor Remoto iniciado na porta %s. Aguardando conexão do agente...", port)
	err := http.ListenAndServe(":"+port, nil)
	if err != nil {
		log.Fatal("ListenAndServe: ", err)
	}
}