// Local: cmd/local-agent/main.go

package main

import (
	"log"
	"time"
	"github.com/gorilla/websocket"
)

func main() {
	// Endereço do nosso servidor remoto. Por enquanto, é local para testes.
	// IMPORTANTE: O 'ws://' é para HTTP e 'wss://' é para HTTPS.
	addr := "ws://localhost:8080/agent"

	log.Printf("AGENT] Tentando se conectar a %s", addr)

	// Loop infinito para garantir que o agente sempre tente se reconectar.
	for {
		c, _, err := websocket.DefaultDialer.Dial(addr, nil)
		if err != nil {
			log.Println("[AGENT] ❌ Falha ao conectar, tentando novamente em 5 segundos:", err)
			time.Sleep(5 * time.Second)
			continue
		}
		log.Println("[AGENT] ✅ Conectado ao Servidor Remoto!")
		defer c.Close()

		// Loop para manter a conexão viva e, no futuro, ler comandos do servidor.
		for {
			_, message, err := c.ReadMessage()
			if err != nil {
				log.Println("[AGENT] 🔌 Desconectado do servidor:", err)
				break // Sai do loop interno para tentar reconectar.
			}
			log.Printf("[AGENT] << Comando recebido do servidor: %s", message)
		}
	}
}