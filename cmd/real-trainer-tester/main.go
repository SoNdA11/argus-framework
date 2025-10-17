// real-trainer-tester: uma ferramenta para medir a latência de RTT de um rolo de treino real.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"encoding/hex"

	"github.com/go-ble/ble"
	"github.com/go-ble/ble/linux"
)

// --- CORREÇÃO: Usando a característica CPS Control Point ---
var CPSControlPointCharUUID = ble.MustParse("00002a66-0000-1000-8000-00805f9b34fb")

// --- CORREÇÃO: Usando os Op Codes corretos para o CPS ---
var PING_COMMAND = []byte{0x04} // Op Code 4: Request Control
var PONG_RESPONSE = []byte{0x20, 0x04, 0x01} // Op Code 32: Response, para Comando 4, com Sucesso 1

func main() {
	mac := flag.String("mac", "", "MAC Address do rolo de treino real")
	adapterID := flag.Int("adapter", 0, "ID do adaptador HCI (ex: hci0)")
	samples := flag.Int("n", 10, "Número de amostras de latência a coletar")
	flag.Parse()

	if *mac == "" { log.Fatalf("❌ O argumento --mac é obrigatório.") }

	log.Printf("🔎 Iniciando Teste de Latência para Rolo Real: %s...", *mac)

	d, err := linux.NewDevice(ble.OptDeviceID(*adapterID))
	if err != nil { log.Fatalf("❌ Falha ao selecionar adaptador: %s", err) }
	ble.SetDefaultDevice(d)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() { <-sig; cancel() }()

	fmt.Printf("📡 Procurando por %s...\n", *mac)
	client, err := ble.Connect(ctx, func(a ble.Advertisement) bool {
		return strings.EqualFold(a.Addr().String(), *mac)
	})
	if err != nil { log.Fatalf("❌ Falha ao conectar: %s.", err) }
	fmt.Println("✅ Conectado!")
	defer client.CancelConnection()

	profile, err := client.DiscoverProfile(true)
	if err != nil { log.Fatalf("❌ Falha ao descobrir perfil: %s", err) }

	// --- CORREÇÃO: Procurando pela característica 0x2A66 ---
	cp := FindCharacteristic(profile, CPSControlPointCharUUID.String())
	if cp == nil { log.Fatalf("❌ Característica de Controle CPS (0x2A66) não encontrada.") }

	pongChan := make(chan struct{})

	fmt.Println("🔔 Inscrevendo-se para receber respostas CPS...")
	if err := client.Subscribe(cp, true, func(data []byte) {
		if bytes.Equal(data, PONG_RESPONSE) {
			pongChan <- struct{}{}
		} else {
			// Log para vermos se o rolo responde com algo diferente
			log.Printf("[TESTER] Resposta inesperada recebida: 0x%s", hex.EncodeToString(data))
		}
	}); err != nil {
		log.Fatalf("❌ Falha ao se inscrever: %s", err)
	}
	defer client.Unsubscribe(cp, true)
	
	time.Sleep(2 * time.Second)

	latencies := []time.Duration{}
	fmt.Printf("🚀 Iniciando teste de latência (coletando %d amostras)...\n", *samples)
	fmt.Println("--------------------------------------------------")

	for i := 0; i < *samples; i++ {
		fmt.Printf("   Amostra %d/%d... ", i+1, *samples)
		startTime := time.Now()
		
		// --- CORREÇÃO: Enviando o comando 0x04 ---
		if err := client.WriteCharacteristic(cp, PING_COMMAND, false); err != nil {
			fmt.Println("Erro ao enviar ping:", err)
			continue
		}

		select {
		case <-pongChan:
			latency := time.Since(startTime)
			latencies = append(latencies, latency)
			fmt.Printf("Pong recebido! Latência: %v\n", latency)
		case <-time.After(3 * time.Second):
			fmt.Println("Timeout! Nenhuma resposta recebida.")
		case <-ctx.Done():
			fmt.Println("Teste cancelado.")
			return
		}
		time.Sleep(1 * time.Second)
	}
	
	printLatencyStats(latencies)
}

func printLatencyStats(latencies []time.Duration) {
	if len(latencies) == 0 {
		fmt.Println("Nenhum dado de latência foi coletado.")
		return
	}
	var sum time.Duration
	min := time.Hour
	max := time.Microsecond
	for _, l := range latencies {
		sum += l
		if l < min { min = l }
		if l > max { max = l }
	}
	mean := sum / time.Duration(len(latencies))
	var varianceSum float64
	for _, l := range latencies {
		varianceSum += math.Pow(float64(l-mean), 2)
	}
	stdDev := time.Duration(math.Sqrt(varianceSum / float64(len(latencies))))
	
	fmt.Println("\n--- Relatório Final de Latência (RTT) ---")
	fmt.Println("--------------------------------------------------")
	fmt.Printf("Total de Amostras: %d\n", len(latencies))
	fmt.Printf("Latência Média:    %v\n", mean)
	fmt.Printf("Latência Mínima:   %v\n", min)
	fmt.Printf("Latência Máxima:   %v\n", max)
	fmt.Printf("Jitter (Desv. Padrão): %v\n", stdDev)
	fmt.Println("--------------------------------------------------")
	
	if stdDev > 20 * time.Millisecond {
		fmt.Println("🚨 Conclusão: Jitter ALTO. Altamente provável que seja um proxy (MitM).")
	} else if mean > 100 * time.Millisecond {
		fmt.Println("⚠️  Conclusão: Latência ALTA. Pode ser um proxy remoto ou uma conexão ruim.")
	} else {
		fmt.Println("✅ Conclusão: Latência e Jitter baixos. Consistente com hardware real.")
	}
}

func FindCharacteristic(p *ble.Profile, uuidStr string) *ble.Characteristic {
	targetUUID := strings.ToLower(strings.ReplaceAll(uuidStr, "-", ""))
	for _, s := range p.Services {
		for _, c := range s.Characteristics {
			foundUUID := strings.ToLower(strings.ReplaceAll(c.UUID.String(), "-", ""))
			if strings.Contains(targetUUID, foundUUID) {
				return c
			}
		}
	}
	return nil
}