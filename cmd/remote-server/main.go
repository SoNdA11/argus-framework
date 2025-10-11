package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

// --- ESTRUTURAS DE ESTADO (Cérebro do Servidor) ---
type BotConfig struct {
	sync.RWMutex
	PowerMin, PowerMax, CadenceMin, CadenceMax int
}
type UIState struct {
	sync.RWMutex
	MainMode       string
	AppConnected   bool
	AgentConnected bool
	ModifiedPower  int
	HeartRate      int
}
type AgentCommand struct {
	Action  string                 `json:"action"`
	Payload map[string]interface{} `json:"payload"`
}
type AgentEvent struct {
	Event   string                 `json:"event"`
	Payload map[string]interface{} `json:"payload"`
}

// --- VARIÁVEIS GLOBAIS DO SERVIDOR ---
var (
	upgrader           = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	agentConn          *websocket.Conn
	agentMutex         sync.Mutex
	dashboardClients   = make(map[*websocket.Conn]bool)
	dashboardMutex     sync.Mutex
	botCfg             = &BotConfig{PowerMin: 70, PowerMax: 110, CadenceMin: 85, CadenceMax: 95}
	uiState            = &UIState{MainMode: "bot"}
)

// botLogicRoutine é a goroutine que gera os dados do bot e os envia para o agente.
func botLogicRoutine(ctx context.Context) {
	var botPowerTarget, botCadenceTarget int
	var botPowerNextChange, botCadenceNextChange time.Time
	powerTicker := time.NewTicker(1 * time.Second)
	cadenceTicker := time.NewTicker(2 * time.Second)
	defer powerTicker.Stop()
	defer cadenceTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-powerTicker.C:
			if uiState.MainMode != "bot" {
				continue
			}
			if time.Now().After(botPowerNextChange) {
				botCfg.RLock()
				pMin, pMax := botCfg.PowerMin, botCfg.PowerMax
				botCfg.RUnlock()
				if pMax > pMin {
					botPowerTarget = rand.Intn(pMax-pMin+1) + pMin
				} else {
					botPowerTarget = pMin
				}
				interval := rand.Intn(16) + 15
				botPowerNextChange = time.Now().Add(time.Duration(interval) * time.Second)
				log.Printf("[BOT] Novo alvo de potência: %dW", botPowerTarget)
			}
			ruido := rand.Intn(5) - 2
			potenciaFinal := botPowerTarget + ruido
			if potenciaFinal < 0 {
				potenciaFinal = 0
			}
			uiState.Lock()
			uiState.ModifiedPower = potenciaFinal
			uiState.Unlock()
			sendCommandToAgent("send_power", map[string]interface{}{"watts": potenciaFinal})

		case <-cadenceTicker.C:
			if uiState.MainMode != "bot" {
				continue
			}
			if time.Now().After(botCadenceNextChange) {
				botCfg.RLock()
				cMin, cMax := botCfg.CadenceMin, botCfg.CadenceMax
				botCfg.RUnlock()
				if cMax > cMin {
					botCadenceTarget = rand.Intn(cMax-cMin+1) + cMin
				} else {
					botCadenceTarget = cMin
				}
				interval := rand.Intn(21) + 20
				botCadenceNextChange = time.Now().Add(time.Duration(interval) * time.Second)
				log.Printf("[BOT] Novo alvo de cadência: %d RPM", botCadenceTarget)
			}
			cadenciaFinal := botCadenceTarget + rand.Intn(3) - 1
			sendCommandToAgent("send_cadence", map[string]interface{}{"rpm": cadenciaFinal})
		}
	}
}

// broadcastToDashboards AGORA ENVIA TODOS OS DADOS DE STATUS
func broadcastToDashboards(ctx context.Context) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done(): return
		case <-ticker.C:
			dashboardMutex.Lock()
			uiState.RLock()
			// Monta a mensagem com TODOS os campos que a UI precisa
			msg, _ := json.Marshal(map[string]interface{}{
				"type":           "statusUpdate",
				"modifiedPower":  uiState.ModifiedPower,
				"heartRate":      uiState.HeartRate,
				"appConnected":   uiState.AppConnected,   
				"agentConnected": uiState.AgentConnected,
				"realPower":      0, 
			})
			uiState.RUnlock()
			for client := range dashboardClients {
				if err := client.WriteMessage(websocket.TextMessage, msg); err != nil {
					client.Close(); delete(dashboardClients, client)
				}
			}
			dashboardMutex.Unlock()
		}
	}
}

// sendCommandToAgent usa a variável 'agentConn' para enviar um comando.
func sendCommandToAgent(action string, payload map[string]interface{}) {
	agentMutex.Lock()
	defer agentMutex.Unlock()
	if agentConn != nil {
		cmd := AgentCommand{Action: action, Payload: payload}
		if err := agentConn.WriteJSON(cmd); err != nil {
			log.Printf("[SERVER] Erro ao enviar comando para o agente: %v", err)
		}
	}
}

// handleAgentConnections gerencia a conexão do 'local-agent'.
func handleAgentConnections(w http.ResponseWriter, r *http.Request) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil { log.Printf("Erro no upgrade do agente: %v", err); return }
	
	agentMutex.Lock(); agentConn = ws; agentMutex.Unlock()
	uiState.Lock(); uiState.AgentConnected = true; uiState.Unlock()
	log.Println("[SERVER] ✅ Agente local conectado!")

	sendCommandToAgent("start_virtual_trainer", map[string]interface{}{"name": "Argus Cloud Trainer"})

	for {
		var event AgentEvent
		if err := ws.ReadJSON(&event); err != nil {
			log.Println("[SERVER] 🔌 Agente local desconectado:", err)
			agentMutex.Lock(); agentConn = nil; agentMutex.Unlock()
			uiState.Lock(); uiState.AgentConnected = false; uiState.AppConnected = false; uiState.Unlock()
			break
		}

		// Processa o evento 'app_status' vindo do agente
		if event.Event == "app_status" {
			if connected, ok := event.Payload["connected"].(bool); ok {
				uiState.Lock()
				uiState.AppConnected = connected
				uiState.Unlock()
				if connected { log.Println("[SERVER] 📲 Agente informou: App de ciclismo CONECTADO.") } else { log.Println("[SERVER] 📲 Agente informou: App de ciclismo DESCONECTADO.") }
			}
		}
	}
}

// handleDashboardConnections gerencia as conexões do dashboard web.
func handleDashboardConnections(w http.ResponseWriter, r *http.Request, cancel context.CancelFunc) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil { log.Println(err); return }
	
	dashboardMutex.Lock(); dashboardClients[conn] = true; dashboardMutex.Unlock()
	log.Printf("[WEB] Novo dashboard conectado: %s", conn.RemoteAddr())

	defer func() {
		dashboardMutex.Lock(); delete(dashboardClients, conn); dashboardMutex.Unlock()
		conn.Close()
		log.Printf("[WEB] Dashboard desconectado: %s", conn.RemoteAddr())
	}()

	for { // Loop para ler comandos do dashboard
		var msg map[string]interface{}
		if err := conn.ReadJSON(&msg); err != nil { break }
		if msgType, ok := msg["type"].(string); ok {
			switch msgType {
			case "setBotConfig":
				if payload, ok := msg["payload"].(map[string]interface{}); ok {
					botCfg.Lock()
					if v, ok := payload["powerMin"].(float64); ok { botCfg.PowerMin = int(v) }
					if v, ok := payload["powerMax"].(float64); ok { botCfg.PowerMax = int(v) }
					if v, ok := payload["cadenceMin"].(float64); ok { botCfg.CadenceMin = int(v) }
					if v, ok := payload["cadenceMax"].(float64); ok { botCfg.CadenceMax = int(v) }
					botCfg.Unlock()
				}
			case "shutdown":
				fmt.Println("[WEB] Comando de desligamento recebido!")
				cancel()
			}
		}
	}
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	go func() { 
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		<-sig
		cancel()
	}()

	go botLogicRoutine(ctx)
	go broadcastToDashboards(ctx)

	http.HandleFunc("/agent", handleAgentConnections)
	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		handleDashboardConnections(w, r, cancel)
	})
	http.Handle("/", http.FileServer(http.Dir("./web")))

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Printf("🚀 Servidor Remoto iniciado na porta %s...", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil && err != http.ErrServerClosed {
		log.Fatal("ListenAndServe: ", err)
	}

	log.Println("Servidor encerrado.")
}