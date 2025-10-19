package main

import (
	"context"
	"encoding/json"
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
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

// --- ESTRUTURAS DE ESTADO ---
type BotConfig struct {
	sync.RWMutex
	PowerMin, PowerMax, CadenceMin, CadenceMax int
}

type AttackConfig struct {
	sync.RWMutex
	Active   bool
	Mode     string
	ValueMin int
	ValueMax int
}

type UIState struct {
	sync.RWMutex
	MainMode            string
	AppConnected        bool
	AgentConnected      bool
	ModifiedPower       int
	HeartRate           int
	RealPower           int
	RealCadence         int
	CurrentBoostTarget  int
	NextBoostChangeTime time.Time
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
	Agents        map[string]*websocket.Conn
	Dashboards    map[string]map[*websocket.Conn]bool
	UserStates    map[string]*UIState
	BotConfigs    map[string]*BotConfig
	AttackConfigs map[string]*AttackConfig
}

var (
	upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	sessionManager = &SessionManager{
		Agents:        make(map[string]*websocket.Conn),
		Dashboards:    make(map[string]map[*websocket.Conn]bool),
		UserStates:    make(map[string]*UIState),
		BotConfigs:    make(map[string]*BotConfig),
		AttackConfigs: make(map[string]*AttackConfig),
	}
)

// isValidAgentKey verifica se uma chave existe no DB
func isValidAgentKey(agentKey string) (bool, *User) {
	if database.DB == nil {
		log.Println("[AUTH] Erro: DB não disponível.")
		return false, nil
	}
	collection := database.DB.Database("argus-db").Collection("users")
	var user User
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := collection.FindOne(ctx, bson.M{"agent_key": agentKey}).Decode(&user)
	if err == nil {
		return true, &user
	}
	if err != mongo.ErrNoDocuments {
		log.Printf("[AUTH] Erro ao verificar chave: %v", err)
	}
	return false, nil
}

// botLogicRoutine (MODIFICADO para acessar mapas de sessão)
func botLogicRoutine(ctx context.Context, agentKey string) {
	log.Printf("[Bot %s] Rotina do bot iniciada.", agentKey)
	var botPowerTarget, botCadenceTarget int
	var botPowerNextChange, botCadenceNextChange time.Time

	botCfg := sessionManager.BotConfigs[agentKey]
	uiState := sessionManager.UserStates[agentKey]

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

			if mode != "bot" || !agentIsConnected {
				continue
			}

			// Lógica de Potência
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
				log.Printf("[BOT %s] Novo alvo de potência: %dW", agentKey, botPowerTarget)
			}
			ruido := rand.Intn(5) - 2
			potenciaFinal := botPowerTarget + ruido
			if potenciaFinal < 0 {
				potenciaFinal = 0
			}
			uiState.Lock()
			uiState.ModifiedPower = potenciaFinal
			uiState.Unlock()
			sendCommandToAgent(agentKey, "send_power", map[string]interface{}{"watts": potenciaFinal})

			// Lógica de Cadência
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
				log.Printf("[BOT %s] Novo alvo de cadência: %d RPM", agentKey, botCadenceTarget)
			}
			cadenciaFinal := botCadenceTarget + rand.Intn(3) - 1
			sendCommandToAgent(agentKey, "send_cadence", map[string]interface{}{"rpm": cadenciaFinal})
		}
	}
}

// broadcastToDashboards (MODIFICADO para acessar mapas de sessão)
func broadcastToDashboards(ctx context.Context, agentKey string) {
	log.Printf("[Broadcast %s] Rotina de broadcast iniciada.", agentKey)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	uiState, ok := sessionManager.UserStates[agentKey]
	if !ok {
		return
	}

	for {
		select {
		case <-ctx.Done():
			log.Printf("[Broadcast %s] Encerrando rotina.", agentKey)
			return
		case <-ticker.C:
			sessionManager.RLock()
			dashboards, userExists := sessionManager.Dashboards[agentKey]
			sessionManager.RUnlock()
			if !userExists {
				continue
			}

			uiState.RLock()
			msg, _ := json.Marshal(map[string]interface{}{
				"type":           "statusUpdate",
				"modifiedPower":  uiState.ModifiedPower,
				"heartRate":      uiState.HeartRate,
				"appConnected":   uiState.AppConnected,
				"agentConnected": uiState.AgentConnected,
				"realPower":      uiState.RealPower,
				"realCadence":    uiState.RealCadence,
			})
			uiState.RUnlock()

			sessionManager.Lock()
			for client := range dashboards {
				if err := client.WriteMessage(websocket.TextMessage, msg); err != nil {
					client.Close()
					delete(dashboards, client)
				}
			}
			sessionManager.Unlock()
		}
	}
}

// sendCommandToAgent (MODIFICADO para acessar mapa de sessão)
func sendCommandToAgent(agentKey string, action string, payload map[string]interface{}) {
	sessionManager.RLock()
	conn, ok := sessionManager.Agents[agentKey]
	sessionManager.RUnlock()
	if ok && conn != nil {
		if err := conn.WriteJSON(AgentCommand{Action: action, Payload: payload}); err != nil {
			log.Printf("[SERVER] Erro envio cmd para %s: %v", agentKey, err)
		}
	}
}

// handleAgentConnections (MODIFICADO para gerenciar sessões)
func handleAgentConnections(w http.ResponseWriter, r *http.Request, mainCtx context.Context) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Erro upgrade agente: %v", err)
		return
	}
	defer ws.Close()

	var authMsg map[string]string
	if err := ws.ReadJSON(&authMsg); err != nil || authMsg["agent_key"] == "" {
		log.Println("[SERVER] 🔌 Falha auth agente.")
		return
	}
	agentKey := authMsg["agent_key"]
	isValid, user := isValidAgentKey(agentKey)
	if !isValid {
		log.Printf("[SERVER] 🔌 Chave inválida: %s", agentKey)
		return
	}

	sessionManager.Lock()
	sessionManager.Agents[agentKey] = ws
	if _, ok := sessionManager.UserStates[agentKey]; !ok {
		log.Printf("[SERVER] Criando nova sessão para o agente %s.", agentKey)
		sessionManager.UserStates[agentKey] = &UIState{MainMode: "boost"}
		sessionManager.BotConfigs[agentKey] = &BotConfig{PowerMin: 180, PowerMax: 220, CadenceMin: 85, CadenceMax: 95}
		sessionManager.AttackConfigs[agentKey] = &AttackConfig{Active: true, Mode: "aditivo", ValueMin: 30, ValueMax: 70}
		sessionManager.Dashboards[agentKey] = make(map[*websocket.Conn]bool)
	}
	uiState := sessionManager.UserStates[agentKey]
	attackCfg := sessionManager.AttackConfigs[agentKey]
	uiState.AgentConnected = true
	sessionManager.Unlock()

	log.Printf("[SERVER] ✅ Agente %s (Usuário: %s) conectado!", agentKey, user.Username)
	sendCommandToAgent(agentKey, "set_mode", map[string]interface{}{"mode": uiState.MainMode})
	sendCommandToAgent(agentKey, "start_virtual_trainer", map[string]interface{}{"name": "Argus Cloud Trainer"})

	sessionCtx, cancel := context.WithCancel(mainCtx)
	go botLogicRoutine(sessionCtx, agentKey)
	go broadcastToDashboards(sessionCtx, agentKey)

	for {
		var event AgentEvent
		if err := ws.ReadJSON(&event); err != nil {
			log.Println("[SERVER] 🔌 Agente", agentKey, "desconectado:", err)
			break
		}

		switch event.Event {
		case "app_status":
			if connected, ok := event.Payload["connected"].(bool); ok {
				uiState.Lock()
				uiState.AppConnected = connected
				uiState.Unlock()
				log.Printf("[SERVER] 📲 Agente %s: App %t.", agentKey, connected)
			}
		case "trainer_data":
			if power, ok := event.Payload["real_power"].(float64); ok {
				realPower := int(power)
				modifiedPower := realPower
				if cadence, ok := event.Payload["real_cadence"].(float64); ok {
					uiState.Lock()
					uiState.RealCadence = int(cadence)
					uiState.Unlock()
				}

				uiState.RLock()
				currentMode := uiState.MainMode
				uiState.RUnlock()

				if currentMode == "boost" {
					attackCfg.RLock()
					isActive, mode, vMin, vMax := attackCfg.Active, attackCfg.Mode, attackCfg.ValueMin, attackCfg.ValueMax
					attackCfg.RUnlock()
					if isActive {
						uiState.Lock()
						if time.Now().After(uiState.NextBoostChangeTime) {
							if vMax > vMin {
								uiState.CurrentBoostTarget = rand.Intn(vMax-vMin+1) + vMin
							} else {
								uiState.CurrentBoostTarget = vMin
							}
							interval := rand.Intn(16) + 15
							uiState.NextBoostChangeTime = time.Now().Add(time.Duration(interval) * time.Second)
							log.Printf("[BOOST %s] Novo alvo: %d (%s) por %ds", agentKey, uiState.CurrentBoostTarget, mode, interval)
						}
						currentBoost := uiState.CurrentBoostTarget
						uiState.Unlock()
						switch mode {
						case "aditivo":
							modifiedPower = realPower + currentBoost
						case "percentual":
							modifiedPower = realPower + int(float64(realPower)*(float64(currentBoost)/100.0))
						}
						if realPower > 0 {
							ruido := rand.Intn(5) - 2
							modifiedPower += ruido
							if modifiedPower < 0 {
								modifiedPower = 0
							}
						}
					}
				}
				if currentMode != "bot" {
					uiState.Lock()
					uiState.RealPower = realPower
					uiState.ModifiedPower = modifiedPower
					uiState.Unlock()
					sendCommandToAgent(agentKey, "send_power", map[string]interface{}{"watts": modifiedPower})
				} else {
					uiState.Lock()
					uiState.RealPower = realPower
					uiState.Unlock()
				}
			}
		}
	}

	cancel()
	sessionManager.Lock()
	delete(sessionManager.Agents, agentKey)
	if uiState, ok := sessionManager.UserStates[agentKey]; ok {
		uiState.AgentConnected = false
		uiState.AppConnected = false
		uiState.RealPower = 0
		uiState.RealCadence = 0
		uiState.ModifiedPower = 0
	}
	log.Printf("[SERVER] Sessão do agente %s encerrada.", agentKey)
	sessionManager.Unlock()
}

// handleDashboardConnections (MODIFICADO para ler agentKey da URL)
func handleDashboardConnections(w http.ResponseWriter, r *http.Request) {
	agentKey := r.URL.Query().Get("agentKey")
	if agentKey == "" {
		log.Println("[WEB] 🔌 Conexão recusada: agentKey não fornecida.")
		http.Error(w, "agentKey parameter is required", http.StatusBadRequest)
		return
	}

	sessionManager.RLock()
	_, agentSessionExists := sessionManager.UserStates[agentKey]
	sessionManager.RUnlock()

	if !agentSessionExists {
		log.Printf("[WEB] 🔌 Conexão recusada: Nenhuma sessão para %s.", agentKey)
		http.Error(w, "No active session for this agentKey. Please connect your agent first.", http.StatusNotFound)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println(err)
		return
	}
	defer conn.Close()

	sessionManager.Lock()
	sessionManager.Dashboards[agentKey][conn] = true
	sessionManager.Unlock()
	log.Printf("[WEB] Novo dashboard para sessão %s", agentKey)
	defer func() {
		sessionManager.Lock()
		delete(sessionManager.Dashboards[agentKey], conn)
		sessionManager.Unlock()
		log.Printf("[WEB] Dashboard desconectado de %s", agentKey)
	}()

	for {
		var msg map[string]interface{}
		if err := conn.ReadJSON(&msg); err != nil {
			break
		}
		log.Printf("[WEB] Comando (sessão %s): %v", agentKey, msg)

		botCfg := sessionManager.BotConfigs[agentKey]
		uiState := sessionManager.UserStates[agentKey]
		attackCfg := sessionManager.AttackConfigs[agentKey]

		if msgType, ok := msg["type"].(string); ok {
			switch msgType {
			case "setMainMode":
				if payload, ok := msg["payload"].(map[string]interface{}); ok {
					if mode, ok := payload["mode"].(string); ok {
						uiState.Lock()
						uiState.MainMode = mode
						uiState.Unlock()
						sendCommandToAgent(agentKey, "set_mode", map[string]interface{}{"mode": mode})
					}
				}
			case "setBotConfig":
				if payload, ok := msg["payload"].(map[string]interface{}); ok {
					botCfg.Lock()
					if v, ok := payload["powerMin"].(float64); ok {
						botCfg.PowerMin = int(v)
					}
					if v, ok := payload["powerMax"].(float64); ok {
						botCfg.PowerMax = int(v)
					}
					if v, ok := payload["cadenceMin"].(float64); ok {
						botCfg.CadenceMin = int(v)
					}
					if v, ok := payload["cadenceMax"].(float64); ok {
						botCfg.CadenceMax = int(v)
					}
					botCfg.Unlock()
				}
			case "setPowerConfig":
				if payload, ok := msg["payload"].(map[string]interface{}); ok {
					attackCfg.Lock()
					if active, ok := payload["active"].(bool); ok {
						attackCfg.Active = active
					}
					if mode, ok := payload["mode"].(string); ok {
						attackCfg.Mode = mode
					}
					if vMin, ok := payload["valueMin"].(float64); ok {
						attackCfg.ValueMin = int(vMin)
					}
					if vMax, ok := payload["valueMax"].(float64); ok {
						attackCfg.ValueMax = int(vMax)
					}
					attackCfg.Unlock()
					log.Printf("[WEB] Config Boost para %s: %+v", agentKey, attackCfg)
				}
			}
		}
	}
}

// handleRegisterRequest processa a criação de novos usuários.
func handleRegisterRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Método não permitido", http.StatusMethodNotAllowed)
		return
	}

	// 1. Decodificar o JSON vindo do frontend
	var user User // Reutiliza a struct User já definida no seu main.go
	if err := json.NewDecoder(r.Body).Decode(&user); err != nil {
		http.Error(w, "JSON inválido", http.StatusBadRequest)
		return
	}

	// 2. Validar input básico
	if user.Username == "" || user.AgentKey == "" {
		http.Error(w, "Usuário e AgentKey são obrigatórios", http.StatusBadRequest)
		return
	}

	// 3. Conectar ao banco
	collection := database.DB.Database("argus-db").Collection("users")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 4. Verificar se o usuário ou a agentKey já existem
	var existingUser User
	err := collection.FindOne(ctx, bson.M{"$or": []bson.M{
		{"username": user.Username},
		{"agent_key": user.AgentKey},
	}}).Decode(&existingUser)

	// Se err == nil, significa que encontrou um documento, então o usuário já existe.
	if err == nil {
		http.Error(w, "Usuário ou AgentKey já cadastrado.", http.StatusConflict) // 409 Conflict
		return
	}
	// Se o erro NÃO for "documento não encontrado", foi um erro real do DB
	if err != mongo.ErrNoDocuments {
		log.Printf("[REGISTER] Erro ao verificar usuário: %v", err)
		http.Error(w, "Erro interno ao verificar usuário", http.StatusInternalServerError)
		return
	}

	// 5. Inserir o novo usuário no banco
	_, err = collection.InsertOne(ctx, user)
	if err != nil {
		log.Printf("[REGISTER] Erro ao criar usuário: %v", err)
		http.Error(w, "Erro interno ao criar usuário", http.StatusInternalServerError)
		return
	}

	// 6. Enviar resposta de sucesso
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated) // 201 Created
	json.NewEncoder(w).Encode(map[string]string{"message": "Usuário criado com sucesso!"})
	log.Printf("[REGISTER] Novo usuário criado: %s", user.Username)
}

func main() {

	database.InitDB()

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		<-sig
		log.Println("Sinal recebido, encerrando...")
		cancel()
	}()

	http.HandleFunc("/register", handleRegisterRequest) 

	http.HandleFunc("/agent", func(w http.ResponseWriter, r *http.Request) { handleAgentConnections(w, r, ctx) })

	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) { handleDashboardConnections(w, r) })

	http.Handle("/", http.FileServer(http.Dir("./cmd/remote-server/web")))

	port := os.Getenv("PORT")

	if port == "" {
		port = "8080"
	}

	log.Printf("🚀 Servidor Remoto na porta %s...", port)

	server := &http.Server{Addr: ":" + port}

	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("ListenAndServe: %s", err)
		}
	}()

	<-ctx.Done()

	log.Println("Desligando servidor...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)

	defer shutdownCancel()

	server.Shutdown(shutdownCtx)

	log.Println("Servidor encerrado.")
}