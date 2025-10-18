package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-ble/ble"
	"github.com/go-ble/ble/linux"
	"github.com/gorilla/websocket"
)

type AgentCommand struct {
	Action  string                 `json:"action"`
	Payload map[string]interface{} `json:"payload"`
}
type AgentEvent struct {
	Event   string                 `json:"event"`
	Payload map[string]interface{} `json:"payload"`
}

var (
	PowerSvcUUID           = ble.MustParse("00001818-0000-1000-8000-00805f9b34fb")
	PowerCharUUID          = ble.MustParse("00002a63-0000-1000-8000-00805f9b34fb")
	CSCSvcUUID             = ble.MustParse("00001816-0000-1000-8000-00805f9b34fb")
	CSCMeasurementCharUUID = ble.MustParse("00002a5b-0000-1000-8000-00805f9b34fb")
	FTMSSvcUUID            = ble.MustParse("00001826-0000-1000-8000-00805f9b34fb")
)

// discoverAdapters (sem alterações)
func discoverAdapters() (int, int, error) {
	log.Println("[AGENT-DISCOVERY] Procurando por 2 adaptadores BLE disponíveis...")
	var availableIDs []int

	for i := 0; i < 10; i++ {
		d, err := linux.NewDevice(ble.OptDeviceID(i))
		if err != nil {
			log.Printf("[AGENT-DISCOVERY] Adaptador hci%d indisponível. Ignorando. (Erro: %v)", i, err)
			continue
		}
		if err := d.Stop(); err != nil {
			log.Printf("[AGENT-DISCOVERY] Aviso: falha ao fechar o adaptador hci%d após o teste: %v", i, err)
		}

		log.Printf("[AGENT-DISCOVERY] ✅ Adaptador hci%d encontrado e disponível.", i)
		availableIDs = append(availableIDs, i)

		if len(availableIDs) == 2 {
			break
		}
	}

	if len(availableIDs) < 2 {
		return -1, -1, fmt.Errorf("falha na descoberta: Não foi possível encontrar 2 adaptadores BLE disponíveis")
	}

	clientID := availableIDs[0]
	serverID := availableIDs[1]
	log.Printf("[AGENT-DISCOVERY] Atribuição: hci%d (CLIENTE) | hci%d (SERVIDOR)", clientID, serverID)

	return clientID, serverID, nil
}

// writePump (sem alterações)
func writePump(ctx context.Context, c *websocket.Conn, writeChan <-chan interface{}, done chan struct{}) {
	pingTicker := time.NewTicker(30 * time.Second)
	defer func() {
		pingTicker.Stop()
		c.Close()
	}()

	for {
		select {
		case <-ctx.Done():
			log.Println("[AGENT-WS] Encerrando write pump (sinal de interrupção)...")
			c.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
			return
		case <-done:
			log.Println("[AGENT-WS] Encerrando write pump (conexão perdida)...")
			return
		case msg, ok := <-writeChan:
			if !ok {
				log.Println("[AGENT-WS] Canal de escrita fechado.")
				return
			}
			if err := c.WriteJSON(msg); err != nil {
				log.Printf("[AGENT-WS] ❌ Erro ao escrever no websocket: %v", err)
				return
			}
		case <-pingTicker.C:
			if err := c.WriteMessage(websocket.PingMessage, nil); err != nil {
				log.Printf("[AGENT-WS] ❌ Erro ao enviar ping: %v", err)
				return
			}
		}
	}
}

// --- MODIFICADO: manageBLE (Servidor Virtual) ---
// Esta função NÃO define mais o dispositivo padrão.
func manageBLE(ctx context.Context, name string, adapterID int, powerChan <-chan int, cadenceChan <-chan int, writeChan chan<- interface{}) {
	log.Printf("[AGENT-BLE] Iniciando rolo virtual no adaptador hci%d...", adapterID)
	d, err := linux.NewDevice(ble.OptDeviceID(adapterID))
	if err != nil {
		log.Printf("[AGENT-BLE] ❌ Falha ao selecionar adaptador: %s", err)
		writeChan <- AgentEvent{"error", map[string]interface{}{"message": err.Error()}}
		return
	}
	// --- REMOVIDO: ble.SetDefaultDevice(d) ---

	powerSvc := ble.NewService(PowerSvcUUID)
	powerChar := powerSvc.NewCharacteristic(PowerCharUUID)
	// --- MODIFICADO: Usa o 'd' (device) para o HandleNotify ---
	powerChar.HandleNotify(ble.NotifyHandlerFunc(func(req ble.Request, ntf ble.Notifier) {
		log.Printf("[AGENT-BLE] ✅ App %s inscrito para Potência.", req.Conn().RemoteAddr())
		writeChan <- AgentEvent{"app_status", map[string]interface{}{"connected": true}}
		defer func() {
			log.Printf("[AGENT-BLE] 🔌 App %s desinscrito da Potência.", req.Conn().RemoteAddr())
			writeChan <- AgentEvent{"app_status", map[string]interface{}{"connected": false}}
		}()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ntf.Context().Done():
				return
			case watts := <-powerChan:
				powerBytes := make([]byte, 4)
				binary.LittleEndian.PutUint16(powerBytes[2:4], uint16(watts))
				if _, err := ntf.Write(powerBytes); err != nil {
					log.Printf("[AGENT-BLE] Erro ao enviar potência: %v", err)
				}
			}
		}
	}))

	cscSvc := ble.NewService(CSCSvcUUID)
	cscChar := cscSvc.NewCharacteristic(CSCMeasurementCharUUID)
	// --- MODIFICADO: Usa o 'd' (device) para o HandleNotify ---
	cscChar.HandleNotify(ble.NotifyHandlerFunc(func(req ble.Request, ntf ble.Notifier) {
		log.Printf("[AGENT-BLE] ✅ App %s inscrito para Cadência.", req.Conn().RemoteAddr())
		defer log.Printf("[AGENT-BLE] 🔌 App %s desinscrito da Cadência.", req.Conn().RemoteAddr())
		var cumulativeRevolutions uint16
		var lastCrankEventTime uint16
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		var cadenciaAlvo int
		for {
			select {
			case <-ctx.Done():
				return
			case <-ntf.Context().Done():
				return
			case novoAlvo := <-cadenceChan:
				cadenciaAlvo = novoAlvo
			case <-ticker.C:
				if cadenciaAlvo <= 0 {
					continue
				}
				revolutionsInInterval := float64(cadenciaAlvo) / 60.0 / 4.0
				cumulativeRevolutions += uint16(revolutionsInInterval)
				lastCrankEventTime += (1024 / 4)
				flags := byte(0x02)
				buf := new(bytes.Buffer)
				binary.Write(buf, binary.LittleEndian, flags)
				binary.Write(buf, binary.LittleEndian, cumulativeRevolutions)
				binary.Write(buf, binary.LittleEndian, lastCrankEventTime)
				if _, err := ntf.Write(buf.Bytes()); err != nil {
					return
				}
			}
		}
	}))

	d.AddService(powerSvc)
	d.AddService(cscSvc)
	d.AddService(ble.NewService(FTMSSvcUUID))

	log.Printf("[AGENT-BLE] 📣 Anunciando como '%s'...", name)
	if err = d.AdvertiseNameAndServices(ctx, name, PowerSvcUUID, FTMSSvcUUID, CSCSvcUUID); err != nil {
		log.Printf("[AGENT-BLE] Erro ao anunciar: %v", err)
	}
	log.Println("[AGENT-BLE] Anúncio parado.")
}


// --- REESCRITA: manageTrainerConnection (Cliente Real) ---
// Esta função agora DEFINE O DISPOSITIVO PADRÃO e usa ble.Connect
func manageTrainerConnection(ctx context.Context, mac string, adapterID int, writeChan chan<- interface{}) {
	if mac == "" {
		log.Println("[AGENT-TRAINER] ⚠️ MAC do rolo não fornecido (--mac). Apenas o rolo virtual funcionará.")
		return
	}

	log.Printf("[AGENT-TRAINER] Iniciando conexão com o rolo real (%s) via hci%d...", mac, adapterID)

	d, err := linux.NewDevice(ble.OptDeviceID(adapterID))
	if err != nil {
		log.Printf("[AGENT-TRAINER] ❌ Falha ao selecionar adaptador: %s", err)
		return
	}
	
	// --- PONTO CRÍTICO: O CLIENTE agora define o dispositivo padrão ---
	ble.SetDefaultDevice(d)
	// --- FIM DO PONTO CRÍTICO ---

	for {
		if ctx.Err() != nil {
			log.Println("[AGENT-TRAINER] Encerrando conexão com o rolo.")
			return
		}

		log.Printf("[AGENT-TRAINER] 📡 Procurando por %s...", mac)

		// --- MODIFICADO: Usa o filtro e o ble.Connect (global) ---
		advFilter := func(a ble.Advertisement) bool {
			return strings.EqualFold(a.Addr().String(), mac)
		}

		// ble.Connect usa o dispositivo padrão que acabamos de definir
		client, err := ble.Connect(ctx, advFilter)
		if err != nil {
			log.Printf("[AGENT-TRAINER] Falha ao conectar: %v. Tentando novamente.", err)
			time.Sleep(5 * time.Second)
			continue
		}
		// --- FIM DA MODIFICAÇÃO ---


		log.Println("[AGENT-TRAINER] ✅ Conectado ao rolo real!")
        
        // Mantemos o delay de 1s para estabilidade
		log.Println("[AGENT-TRAINER] Aguardando 1s para estabilizar a conexão...")
		time.Sleep(1 * time.Second)

		// A descoberta do perfil agora deve funcionar
		p, err := client.DiscoverProfile(true)
		if err != nil {
			log.Printf("[AGENT-TRAINER] ❌ Falha ao descobrir perfil: %v", err)
			client.CancelConnection()
			continue
		}

		powerChar := findCharacteristic(p, PowerCharUUID)
		if powerChar == nil {
			log.Println("[AGENT-TRAINER] ❌ Característica de potência (2A63) não encontrada no rolo real.")
			client.CancelConnection()
			continue
		}

		log.Println("[AGENT-TRAINER] 🔔 Inscrevendo-se para dados de potência real...")
		if err := client.Subscribe(powerChar, false, func(data []byte) {
			if len(data) >= 4 {
				powerValue := binary.LittleEndian.Uint16(data[2:4])
				writeChan <- AgentEvent{"trainer_data", map[string]interface{}{
					"real_power": int(powerValue),
				}}
			}
		}); err != nil {
			log.Printf("[AGENT-TRAINER] ❌ Falha ao se inscrever: %v", err)
			client.CancelConnection()
			continue
		}

		<-client.Disconnected()
		log.Println("[AGENT-TRAINER] 🔌 Desconectado do rolo real. Tentando reconectar...")
	}
}

// findCharacteristic (sem alterações)
func findCharacteristic(p *ble.Profile, uuid ble.UUID) *ble.Characteristic {
	for _, s := range p.Services {
		for _, c := range s.Characteristics {
			if c.UUID.Equal(uuid) {
				return c
			}
		}
	}
	return nil
}

// main (sem alterações)
func main() {
	agentKey := flag.String("key", "", "Chave de Agente (API Key) para autenticação")
	trainerMAC := flag.String("mac", "", "MAC Address do rolo de treino real (ex: AA:BB:CC:11:22:33)")
	flag.Parse()

	if *agentKey == "" {
		log.Fatal("❌ Erro: A flag --key é obrigatória. Obtenha a chave no seu dashboard.")
	}

	addr := "wss://argus-remote-server.onrender.com/agent"

	clientAdapterID, serverAdapterID, err := discoverAdapters()
	if err != nil {
		log.Fatalf("❌ %v", err)
	}

	log.Printf("[AGENT] Iniciando agente local... Cliente (hci%d) -> Servidor (hci%d)", clientAdapterID, serverAdapterID)

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt, syscall.SIGTERM)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-interrupt; log.Println("Encerrando agente..."); cancel() }()

	powerChan := make(chan int, 10)
	cadenceChan := make(chan int, 10)

	for {
		if ctx.Err() != nil {
			log.Println("Contexto cancelado. Saindo."); return
		}

		log.Printf("[AGENT] Tentando se conectar a %s", addr)
		c, _, err := websocket.DefaultDialer.Dial(addr, nil)
		if err != nil {
			log.Println("❌ Falha...:", err); time.Sleep(5 * time.Second); continue
		}

		log.Println("[AGENT] ✅ Conectado! Autenticando com a Chave de Agente...")
		authMsg := map[string]string{"agent_key": *agentKey}
		if err := c.WriteJSON(authMsg); err != nil {
			log.Println("❌ Falha ao enviar chave de autenticação:", err)
			c.Close(); time.Sleep(5 * time.Second); continue
		}

		writeChan := make(chan interface{}, 10)
		done := make(chan struct{})
		bleCtx, bleCancel := context.WithCancel(ctx)

		go writePump(ctx, c, writeChan, done)

		go func() {
			defer func() { bleCancel(); close(done) }()
			for {
				var cmd AgentCommand
				if err := c.ReadJSON(&cmd); err != nil {
					log.Println("🔌 Erro de leitura:", err); return
				}

				switch cmd.Action {
				case "start_virtual_trainer":
					if name, ok := cmd.Payload["name"].(string); ok {
						// O Cliente (que define o DefaultDevice) é iniciado primeiro.
						go manageTrainerConnection(bleCtx, *trainerMAC, clientAdapterID, writeChan)
						// O Servidor (que usa métodos de dispositivo) é iniciado em seguida.
						go manageBLE(bleCtx, name, serverAdapterID, powerChan, cadenceChan, writeChan)
					}
				case "send_power":
					if watts, ok := cmd.Payload["watts"].(float64); ok {
						select { case powerChan <- int(watts): default: }
					}
				case "send_cadence":
					if rpm, ok := cmd.Payload["rpm"].(float64); ok {
						select { case cadenceChan <- int(rpm): default: }
					}
				}
			}
		}()

		select {
		case <-done:
			log.Println("[AGENT] Conexão perdida. Tentando reconectar...")
		case <-ctx.Done():
			log.Println("Sinal de encerramento recebido. Fechando conexão...")
			time.Sleep(1 * time.Second)
			return
		}
	}
}