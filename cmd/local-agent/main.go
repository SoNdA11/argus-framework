// Local: cmd/local-agent/main.go

package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"
	"github.com/gorilla/websocket"
)

func main() {
	// ATENÇÃO: Deixe este endereço como localhost por enquanto.
	// Só mudaremos para a URL do Render depois que o deploy funcionar.
	addr := "ws://localhost:8080/agent"

	log.Printf("[AGENT] Iniciando agente local...")

	// Permite encerrar o agente com Ctrl+C
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt, syscall.SIGTERM)

	for {
		log.Printf("[AGENT] Tentando se conectar a %s", addr)
		c, _, err := websocket.DefaultDialer.Dial(addr, nil)
		if err != nil {
			log.Println("[AGENT] ❌ Falha ao conectar, tentando novamente em 5 segundos:", err)
			
			select {
			case <-time.After(5 * time.Second):
				continue
			case <-interrupt:
				log.Println("Encerrando agente.")
				return
			}
		}
		
		log.Println("[AGENT] ✅ Conectado ao Servidor Remoto!")
		
		done := make(chan struct{})
		go func() {
			defer close(done)
			for {
				_, message, err := c.ReadMessage()
				if err != nil {
					log.Println("[AGENT] 🔌 Desconectado do servidor:", err)
					return
				}
				log.Printf("[AGENT] << Comando recebido do servidor: %s", message)
			}
		}()

		select {
		case <-done: // A conexão foi perdida, o loop externo vai tentar reconectar.
		case <-interrupt: // O usuário apertou Ctrl+C.
			log.Println("Encerrando conexão...")
			c.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
			return
		}
	}
}