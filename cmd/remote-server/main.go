package main

import (
	"context"
	"log"
	"math/rand"
	"net/http"
	"os"
	"sync"
	"time"
	"github.com/gorilla/websocket"
)

// --- ESTRUTURAS DE ESTADO (agora vivem no servidor) ---
type BotConfig struct {
	sync.RWMutex
	PowerMin, PowerMax, CadenceMin, CadenceMax int
}
type UIState struct {
	sync.RWMutex
	MainMode      string
	ModifiedPower int
	// ... outros campos podem ser adicionados conforme necessário
}
type AgentCommand struct {
	Action  string                 `json:"action"`
	Payload map[string]interface{} `json:"payload"`
}

// --- VARIÁVEIS GLOBAIS DO SERVIDOR ---
var (
	upgrader    = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	agentConn   *websocket.Conn // Armazena a conexão do agente único
	agentMutex  sync.Mutex
	botCfg      = &BotConfig{PowerMin: 180, PowerMax: 220, CadenceMin: 85, CadenceMax: 95}
	uiState     = &UIState{MainMode: "bot"} // Começamos no modo bot para este exemplo
)

// botLogicRoutine é a goroutine que gera os dados do bot.
func botLogicRoutine(ctx context.Context) {
	var botPowerTarget, botCadenceTarget int
	var botPowerNextChange, botCadenceNextChange time.Time
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			uiState.RLock()
			mainMode := uiState.MainMode
			uiState.RUnlock()

			if mainMode != "bot" {
				continue // Se não estiver no modo bot, não faz nada.
			}

			// Lógica de Potência Dinâmica do Bot
			if time.Now().After(botPowerNextChange) {
				botCfg.RLock()
				pMin, pMax := botCfg.PowerMin, botCfg.PowerMax
				botCfg.RUnlock()
				if pMax > pMin { botPowerTarget = rand.Intn(pMax-pMin+1) + pMin } else { botPowerTarget = pMin }
				interval := rand.Intn(16) + 15
				botPowerNextChange = time.Now().Add(time.Duration(interval) * time.Second)
				log.Printf("[BOT] Novo alvo de potência: %dW", botPowerTarget)
			}
			ruido := rand.Intn(5) - 2
			potenciaFinal := botPowerTarget + ruido
			if potenciaFinal < 0 { potenciaFinal = 0 }
			uiState.Lock(); uiState.ModifiedPower = potenciaFinal; uiState.Unlock()
			sendCommandToAgent("send_power", map[string]interface{}{"watts": potenciaFinal})

			// Lógica de Cadência Dinâmica do Bot
			if time.Now().After(botCadenceNextChange) {
				botCfg.RLock()
				cMin, cMax := botCfg.CadenceMin, botCfg.CadenceMax
				botCfg.RUnlock()
				if cMax > cMin { botCadenceTarget = rand.Intn(cMax-cMin+1) + cMin } else { botCadenceTarget = cMin }
				interval := rand.Intn(21) + 20
				botCadenceNextChange = time.Now().Add(time.Duration(interval) * time.Second)
				log.Printf("[BOT] Novo alvo de cadência: %d RPM", botCadenceTarget)
			}
			cadenciaFinal := botCadenceTarget + rand.Intn(3) - 1
			sendCommandToAgent("send_cadence", map[string]interface{}{"rpm": cadenciaFinal})
		}
	}
}

// sendCommandToAgent envia um comando para o agente local conectado.
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
	if err != nil { log.Fatal(err) }
	
	agentMutex.Lock()
	agentConn = ws
	agentMutex.Unlock()
	log.Println("[SERVER] ✅ Agente local conectado!")

	// Envia o comando inicial para o agente criar o rolo virtual.
	sendCommandToAgent("start_virtual_trainer", map[string]interface{}{"name": "Argus Cloud Trainer"})

	// Loop de leitura para detectar desconexão
	for {
		if _, _, err := ws.ReadMessage(); err != nil {
			log.Println("[SERVER] 🔌 Agente local desconectado:", err)
			agentMutex.Lock()
			agentConn = nil
			agentMutex.Unlock()
			break
		}
	}
}

// handleDashboardConnections gerencia as conexões do dashboard web.
func handleDashboardConnections(w http.ResponseWriter, r *http.Request) {
	// (Esta função será a fusão do seu antigo 'handleWebSocket')
	// ... (código para receber 'setBotConfig', etc. virá aqui no futuro)
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Inicia a "inteligência" do bot em uma goroutine.
	go botLogicRoutine(ctx)

	// Define as rotas
	http.HandleFunc("/agent", handleAgentConnections)
	http.HandleFunc("/ws", handleDashboardConnections)
	http.Handle("/", http.FileServer(http.Dir("./web"))) // Serve o dashboard

	port := os.Getenv("PORT"); if port == "" { port = "8080" }
	log.Printf("🚀 Servidor Remoto iniciado na porta %s...", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatal("ListenAndServe: ", err)
	}
}