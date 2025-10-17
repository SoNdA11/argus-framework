package web

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
	"argus-framework/pkg/ble"
	"github.com/gorilla/websocket"
)

// HubRoutine gerencia o ciclo de vida do servidor web.
func HubRoutine(ctx context.Context, cancel context.CancelFunc, uiState *ble.UIState, powerCfg *ble.AttackConfig, resistanceCfg *ble.ResistanceConfig, wg *sync.WaitGroup) {
	defer wg.Done()
	fmt.Println("[WEB] Goroutine do Hub Web iniciada.")
	server := &http.Server{Addr: ":8080"}

	var upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	var clients = make(map[*websocket.Conn]bool)
	var clientsMutex sync.Mutex

	go broadcastToWebClients(ctx, uiState, &clients, &clientsMutex)

	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		handleWebSocket(w, r, cancel, powerCfg, resistanceCfg, &upgrader, &clients, &clientsMutex)
	})
	http.Handle("/", http.FileServer(http.Dir("./cmd/mitm-proxy/web")))

	go func() {
		fmt.Println("[WEB] Servidor web iniciado em http://localhost:8080")
		if err := server.ListenAndServe(); err != http.ErrServerClosed {
			log.Fatalf("[WEB] ❌ Falha ao iniciar servidor web: %s", err)
		}
	}()

	<-ctx.Done()
	fmt.Println("[WEB] Desligando o servidor web...")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("[WEB] Erro no desligamento do servidor web: %s", err)
	}
	fmt.Println("[WEB] Servidor web desligado.")
}

// broadcastToWebClients envia o estado atual da aplicação para todos os dashboards.
func broadcastToWebClients(ctx context.Context, uiState *ble.UIState, clients *map[*websocket.Conn]bool, clientsMutex *sync.Mutex) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(500 * time.Millisecond):
			clientsMutex.Lock()
			uiState.RLock()
			msg, _ := json.Marshal(map[string]interface{}{
				"type":            "statusUpdate",
				"realPower":       uiState.RealPower,
				"modifiedPower":   uiState.ModifiedPower,
				"heartRate":       uiState.HeartRate,
				"clientConnected": uiState.ClientConnected,
				"appConnected":    uiState.AppConnected,
			})
			uiState.RUnlock()

			for client := range *clients {
				if err := client.WriteMessage(websocket.TextMessage, msg); err != nil {
					client.Close()
					delete(*clients, client)
				}
			}
			clientsMutex.Unlock()
		}
	}
}

func handleWebSocket(w http.ResponseWriter, r *http.Request, cancel context.CancelFunc, powerCfg *ble.AttackConfig, resistanceCfg *ble.ResistanceConfig, upgrader *websocket.Upgrader, clients *map[*websocket.Conn]bool, clientsMutex *sync.Mutex) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println(err)
		return
	}

	clientsMutex.Lock()
	(*clients)[conn] = true
	clientsMutex.Unlock()
	fmt.Printf("[WEB] Novo cliente web conectado: %s\n", conn.RemoteAddr())

	defer func() {
		clientsMutex.Lock()
		delete(*clients, conn)
		clientsMutex.Unlock()
		conn.Close()
		fmt.Printf("[WEB] Cliente web desconectado: %s\n", conn.RemoteAddr())
	}()

	for {
		var msg map[string]interface{}
		if err := conn.ReadJSON(&msg); err != nil {
			break
		}
		if msgType, ok := msg["type"].(string); ok {
			switch msgType {
			case "setPowerConfig":
				if payload, ok := msg["payload"].(map[string]interface{}); ok {
					powerCfg.Lock()
					if active, ok := payload["active"].(bool); ok { powerCfg.Active = active }
					if mode, ok := payload["mode"].(string); ok { powerCfg.Mode = mode }
					if v, ok := payload["valueMin"].(float64); ok { powerCfg.ValueMin = int(v) }
					if v, ok := payload["valueMax"].(float64); ok { powerCfg.ValueMax = int(v) }
					powerCfg.Unlock()
				}
			case "setResistanceConfig":
				if payload, ok := msg["payload"].(map[string]interface{}); ok {
					resistanceCfg.Lock()
					if active, ok := payload["active"].(bool); ok { resistanceCfg.Forward = active }
					resistanceCfg.Unlock()
				}
			case "shutdown":
				fmt.Println("[WEB] Comando de desligamento recebido!")
				cancel()
			}
		}
	}
}