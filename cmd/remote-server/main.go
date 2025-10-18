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

	"argus-framework/pkg/database"
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
type User struct {
	Username string `json:"username" bson:"username"`
	AgentKey string `json:"agent_key" bson:"agent_key"`
}

// --- GERENCIADOR DE SESSÕES ---
type SessionManager struct {
	sync.RWMutex
	Agents     map[string]*websocket.Conn
	Dashboards map[string]map[*websocket.Conn]bool
	UserStates map[string]*UIState
	BotConfigs map[string]*BotConfig
}

// --- VARIÁVEIS GLOBAIS ---
var (
	upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	sessionManager = &SessionManager{
		Agents:     make(map[string]*websocket.Conn),
		Dashboards: make(map[string]map[*websocket.Conn]bool),
		UserStates: make(map[string]*UIState),
		BotConfigs: make(map[string]*BotConfig),
	}
	// CORREÇÃO: A variável 'db' foi removida. Vamos acessá-la via 'database.DB'
)

// botLogicRoutine é iniciada para cada agente conectado
func botLogicRoutine(ctx context.Context, agentKey string) {
	log.Printf("[Bot %s] Rotina do bot iniciada.", agentKey)
	var botPowerTarget, botCadenceTarget int
	var botPowerNextChange, botCadenceNextChange time.Time
	
	botCfg := sessionManager.BotConfigs[agentKey]  // Correção: , _ removido
	uiState := sessionManager.UserStates[agentKey] // Correção: , _ removido
	
	// Corrige o "Bot Atrasado"
	botCfg.RLock()
	if botCfg.PowerMax > botCfg.PowerMin { botPowerTarget = rand.Intn(botCfg.PowerMax-botCfg.PowerMin+1) + botCfg.PowerMin } else { botPowerTarget = botCfg.PowerMin }
	if botCfg.CadenceMax > botCfg.CadenceMin { botCadenceTarget = rand.Intn(botCfg.CadenceMax-botCfg.CadenceMin+1) + botCfg.CadenceMin } else { botCadenceTarget = botCfg.CadenceMin }
	botCfg.RUnlock()
	
	powerTicker := time.NewTicker(1 * time.Second)
	defer powerTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Printf("[Bot %s] Encerrando rotina.", agentKey)
			return
		case <-powerTicker.C:
			uiState.RLock()
			mode := uiState.MainMode
			agentIsConnected := uiState.AgentConnected
			uiState.RUnlock()

			if mode != "bot" || !agentIsConnected { continue }

			// Lógica de Potência Dinâmica
			if time.Now().After(botPowerNextChange) {
				botCfg.RLock(); pMin, pMax := botCfg.PowerMin, botCfg.PowerMax; botCfg.RUnlock()
				if pMax > pMin { botPowerTarget = rand.Intn(pMax-pMin+1) + pMin } else { botPowerTarget = pMin }
				interval := rand.Intn(16) + 15
				botPowerNextChange = time.Now().Add(time.Duration(interval) * time.Second)
				log.Printf("[BOT %s] Novo alvo de potência: %dW", agentKey, botPowerTarget)
			}
			ruido := rand.Intn(5) - 2
			potenciaFinal := botPowerTarget + ruido
			if potenciaFinal < 0 { potenciaFinal = 0 }
			uiState.Lock(); uiState.ModifiedPower = potenciaFinal; uiState.Unlock()
			sendCommandToAgent(agentKey, "send_power", map[string]interface{}{"watts": potenciaFinal})

			// Lógica de Cadência Dinâmica
			if time.Now().After(botCadenceNextChange) {
				botCfg.RLock(); cMin, cMax := botCfg.CadenceMin, botCfg.CadenceMax; botCfg.RUnlock()
				if cMax > cMin { botCadenceTarget = rand.Intn(cMax-cMin+1) + cMin } else { botCadenceTarget = cMin }
				interval := rand.Intn(21) + 20
				botCadenceNextChange = time.Now().Add(time.Duration(interval) * time.Second)
				log.Printf("[BOT %s] Novo alvo de cadência: %d RPM", agentKey, botCadenceTarget)
			}
			cadenciaFinal := botCadenceTarget + rand.Intn(3) - 1
			sendCommandToAgent(agentKey, "send_cadence", map[string]interface{}{"rpm": cadenciaFinal})
		}
	}
}

// broadcastToDashboards envia dados para os dashboards de um usuário
func broadcastToDashboards(ctx context.Context, agentKey string) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	
	uiState, ok := sessionManager.UserStates[agentKey]
	if !ok { log.Printf("[Broadcast %s] Erro: estado de UI não encontrado.", agentKey); return }

	for {
		select {
		case <-ctx.Done():
			log.Printf("[Broadcast %s] Encerrando rotina.", agentKey)
			return
		case <-ticker.C:
			sessionManager.RLock()
			dashboards, userExists := sessionManager.Dashboards[agentKey]
			sessionManager.RUnlock()
			
			if !userExists { continue }

			uiState.RLock()
			msg, _ := json.Marshal(map[string]interface{}{
				"type":           "statusUpdate",
				"modifiedPower":  uiState.ModifiedPower,
				"heartRate":      uiState.HeartRate,
				"appConnected":   uiState.AppConnected,
				"agentConnected": uiState.AgentConnected,
				"realPower":      0, // Placeholder
			})
			uiState.RUnlock()

			sessionManager.Lock()
			for client := range dashboards {
				if err := client.WriteMessage(websocket.TextMessage, msg); err != nil {
					client.Close(); delete(dashboards, client)
				}
			}
			sessionManager.Unlock()
		}
	}
}

// sendCommandToAgent envia um comando para um agente específico
func sendCommandToAgent(agentKey string, action string, payload map[string]interface{}) {
	sessionManager.RLock()
	conn, ok := sessionManager.Agents[agentKey]
	sessionManager.RUnlock()
	if ok && conn != nil {
		cmd := AgentCommand{Action: action, Payload: payload}
		if err := conn.WriteJSON(cmd); err != nil {
			log.Printf("[SERVER] Erro ao enviar comando para o agente %s: %v", agentKey, err)
		}
	}
}

// handleAgentConnections autentica e gerencia um 'local-agent'
func handleAgentConnections(w http.ResponseWriter, r *http.Request, mainCtx context.Context) { // CORREÇÃO: Recebe o mainCtx
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil { log.Printf("Erro no upgrade do agente: %v", err); return }

	var authMsg map[string]string
	if err := ws.ReadJSON(&authMsg); err != nil || authMsg["agent_key"] == "" {
		log.Println("[SERVER] 🔌 Agente falhou na autenticação. Desconectando.")
		ws.Close()
		return
	}
	agentKey := authMsg["agent_key"]
	
	// TODO: Verificar se a agentKey existe no banco de dados

	sessionManager.Lock()
	sessionManager.Agents[agentKey] = ws
	if _, ok := sessionManager.UserStates[agentKey]; !ok {
		sessionManager.UserStates[agentKey] = &UIState{MainMode: "bot"}
		sessionManager.BotConfigs[agentKey] = &BotConfig{PowerMin: 180, PowerMax: 220, CadenceMin: 85, CadenceMax: 95}
		sessionManager.Dashboards[agentKey] = make(map[*websocket.Conn]bool)
	}
	sessionManager.UserStates[agentKey].AgentConnected = true
	sessionManager.Unlock()
	
	log.Printf("[SERVER] ✅ Agente %s conectado!", agentKey)
	sendCommandToAgent(agentKey, "start_virtual_trainer", map[string]interface{}{"name": "Argus Cloud Trainer"})

	// CORREÇÃO: Cria um contexto para esta sessão, que é filho do mainCtx
	sessionCtx, cancel := context.WithCancel(mainCtx) 
	go botLogicRoutine(sessionCtx, agentKey)
	go broadcastToDashboards(sessionCtx, agentKey)

	for {
		var event AgentEvent
		if err := ws.ReadJSON(&event); err != nil {
			log.Println("[SERVER] 🔌 Agente", agentKey, "desconectado:", err)
			break
		}
		if event.Event == "app_status" {
			if connected, ok := event.Payload["connected"].(bool); ok {
				uiState := sessionManager.UserStates[agentKey]
				uiState.Lock(); uiState.AppConnected = connected; uiState.Unlock()
				if connected { log.Println("[SERVER] 📲 Agente", agentKey, "informou: App CONECTADO.") } else { log.Println("[SERVER] 📲 Agente", agentKey, "informou: App DESCONECTADO.") }
			}
		}
	}
	
	cancel() // Para as goroutines de bot e broadcast deste agente
	sessionManager.Lock()
	delete(sessionManager.Agents, agentKey)
	if uiState, ok := sessionManager.UserStates[agentKey]; ok {
		uiState.AgentConnected = false
		uiState.AppConnected = false
	}
	sessionManager.Unlock()
}

// handleDashboardConnections autentica e gerencia um dashboard web
func handleDashboardConnections(w http.ResponseWriter, r *http.Request, cancel context.CancelFunc) {
	agentKey := "paulo_sk_123abc" // Placeholder
	
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil { log.Println(err); return }
	
	sessionManager.Lock()
	if _, ok := sessionManager.Dashboards[agentKey]; !ok {
		sessionManager.Dashboards[agentKey] = make(map[*websocket.Conn]bool)
	}
	sessionManager.Dashboards[agentKey][conn] = true
	sessionManager.Unlock()
	log.Printf("[WEB] Novo dashboard conectado para o agente %s", agentKey)

	defer func() {
		sessionManager.Lock()
		delete(sessionManager.Dashboards[agentKey], conn)
		sessionManager.Unlock()
		conn.Close()
		log.Printf("[WEB] Dashboard desconectado do agente %s", agentKey)
	}()

	for {
		var msg map[string]interface{}
		if err := conn.ReadJSON(&msg); err != nil { break }
		
		log.Printf("[WEB] Comando recebido do dashboard: %v", msg)

		if msgType, ok := msg["type"].(string); ok {
			botCfg := sessionManager.BotConfigs[agentKey]
			uiState := sessionManager.UserStates[agentKey]

			switch msgType {
			case "setMainMode":
				if payload, ok := msg["payload"].(map[string]interface{}); ok {
					if mode, ok := payload["mode"].(string); ok {
						uiState.Lock(); uiState.MainMode = mode; uiState.Unlock()
					}
				}
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
				fmt.Println("[WEB] Comando de desligamento recebido!"); cancel()
			}
		}
	}
}

func main() {
	// CORREÇÃO 1: 'ctx' agora é '_' porque só usamos 'cancel'
	_, cancel := context.WithCancel(context.Background())
	
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		<-sig
		cancel()
	}()

	database.InitDB()
	// CORREÇÃO 2: Removemos 'db = ...' pois a variável global 'db' foi removida.
	
	// CORREÇÃO 3: Passamos o 'ctx' (que precisamos re-declarar) para o handler do agente
	// Temos que declarar 'ctx' novamente, pois o '_' o descartou.
	ctx, cancel := context.WithCancel(context.Background())
	// Reconfigura o 'go func()' para usar o novo 'cancel'
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		<-sig
		log.Println("Sinal de interrupção recebido, encerrando servidor...")
		cancel()
	}()
	
	
	http.HandleFunc("/agent", func(w http.ResponseWriter, r *http.Request) {
		handleAgentConnections(w, r, ctx) // Passa o 'ctx' da main
	})
	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		handleDashboardConnections(w, r, cancel) // Passa o 'cancel' da main
	})
	http.Handle("/", http.FileServer(http.Dir("./cmd/remote-server/web")))

	port := os.Getenv("PORT"); if port == "" { port = "8080" }
	log.Printf("🚀 Servidor Remoto iniciado na porta %s...", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil && err != http.ErrServerClosed {
		log.Fatal("ListenAndServe: ", err)
	}
	
	log.Println("Servidor encerrado.")
}