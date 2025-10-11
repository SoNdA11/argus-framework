
package main

import (
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

	"github.com/go-ble/ble"
	"github.com/go-ble/ble/linux"
)

const (
	powerCharUUIDStr = "00002a63-0000-1000-8000-00805f9b34fb"
	numSamples       = 200
)

func main() {
	macAddress := flag.String("mac", "", "Endereço MAC do dispositivo a ser testado (obrigatório)")
	adapterID := flag.Int("adapter", 0, "ID do adaptador HCI a ser usado (ex: 0 para hci0)")
	discoverMode := flag.Bool("discover", false, "Apenas descobre e lista todos os serviços e características")
	flag.Parse()

	if *macAddress == "" {
		log.Println("Erro: O argumento --mac é obrigatório.")
		flag.Usage()
		os.Exit(1)
	}

	fmt.Printf("🔎 Iniciando Argus Scanner para o dispositivo: %s\n", *macAddress)

	d, err := linux.NewDevice(ble.OptDeviceID(*adapterID))
	if err != nil {
		log.Fatalf("❌ Falha ao selecionar adaptador hci%d: %s", *adapterID, err)
	}
	ble.SetDefaultDevice(d)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		cancel()
	}()

	fmt.Printf("📡 Procurando por %s via hci%d...\n", *macAddress, *adapterID)
	client, err := ble.Connect(ctx, func(a ble.Advertisement) bool {
		return strings.EqualFold(a.Addr().String(), *macAddress)
	})
	if err != nil {
		log.Fatalf("❌ Falha ao conectar: %s.", err)
	}
	fmt.Println("✅ Conectado ao dispositivo!")
	defer client.CancelConnection()

	fmt.Println("🔍 Descobrindo perfil do dispositivo...")
	profile, err := client.DiscoverProfile(true)
	if err != nil {
		log.Fatalf("❌ Falha ao descobrir perfil: %s", err)
	}

	if *discoverMode {
		fmt.Println("--- Modo de Descoberta ---")
		printProfile(profile)
		return
	}

	fmt.Println("--- Modo de Análise de Jitter ---")
	powerChar := findCharacteristic(profile, powerCharUUIDStr) // Usando a nova função corrigida
	if powerChar == nil {
		log.Fatalf("❌ Característica de medição de potência (%s) não encontrada. Tente o modo --discover para ver todas as características disponíveis.", powerCharUUIDStr)
	}
	fmt.Println("🔔 Característica de potência encontrada. Iniciando coleta de dados...")

	intervals := make([]float64, 0, numSamples)
	done := make(chan struct{})
	var lastPacketTime time.Time

	handler := func(data []byte) {
		if lastPacketTime.IsZero() {
			lastPacketTime = time.Now()
			return
		}
		now := time.Now()
		interval := now.Sub(lastPacketTime)
		intervals = append(intervals, float64(interval.Milliseconds()))
		lastPacketTime = now
		fmt.Printf("\r📊 Coletando amostras: %d/%d", len(intervals), numSamples)
		if len(intervals) >= numSamples {
			close(done)
		}
	}

	if err := client.Subscribe(powerChar, false, handler); err != nil {
		log.Fatalf("❌ Falha ao se inscrever: %s", err)
	}
	defer client.Unsubscribe(powerChar, false)

	select {
	case <-done:
		fmt.Println("\n🏁 Coleta de dados finalizada.")
	case <-ctx.Done():
		fmt.Println("\n⚠️ Coleta interrompida.")
		return
	}

	printResults(intervals)
}

func printProfile(p *ble.Profile) {
	fmt.Println("-----------------------------------------")
	for _, s := range p.Services {
		fmt.Printf("Serviço: %s (%s)\n", s.UUID, ble.Name(s.UUID))
		for _, c := range s.Characteristics {
			fmt.Printf("  - Característica: %s (%s), Propriedades: %s\n", c.UUID, ble.Name(c.UUID), c.Property)
		}
	}
	fmt.Println("-----------------------------------------")
}

func printResults(intervals []float64) {
	if len(intervals) < 2 {
		fmt.Println("Não há dados suficientes para análise.")
		return
	}
	var sum, varianceSum float64
	minInterval := math.MaxFloat64
	maxInterval := 0.0
	for _, interval := range intervals {
		sum += interval
		if interval < minInterval {
			minInterval = interval
		}
		if interval > maxInterval {
			maxInterval = interval
		}
	}
	mean := sum / float64(len(intervals))
	for _, interval := range intervals {
		varianceSum += math.Pow(interval - mean, 2)
	}
	stdDev := math.Sqrt(varianceSum / float64(len(intervals)))
	fmt.Println("\n--- Relatório de Análise de Sinal BLE ---")
	fmt.Println("-----------------------------------------")
	fmt.Printf("Total de Amostras Coletadas: %d\n", len(intervals))
	fmt.Printf("Intervalo Médio (Latência):  %.2f ms\n", mean)
	fmt.Printf("Intervalo Mínimo:            %.2f ms\n", minInterval)
	fmt.Printf("Intervalo Máximo:            %.2f ms\n", maxInterval)
	fmt.Printf("Desvio Padrão (Jitter):      %.2f ms\n", stdDev)
	fmt.Println("-----------------------------------------")
	var conclusion string
	if stdDev < 5.0 {
		conclusion = "✅ Jitter MUITO BAIXO. O comportamento é consistente com hardware dedicado (Rolo Real)."
	} else if stdDev >= 5.0 && stdDev < 15.0 {
		conclusion = "⚠️ Jitter MODERADO. Pode ser um dispositivo não-padrão ou um sistema com muita interferência."
	} else {
		conclusion = "🚨 Jitter ALTO. O comportamento é altamente consistente com virtualização baseada em software (Proxy MitM)."
	}
	fmt.Printf("Conclusão: %s\n", conclusion)
}

// --- FUNÇÃO CORRIGIDA AQUI ---
// Esta versão é mais flexível e robusta para encontrar características.
func findCharacteristic(p *ble.Profile, uuidStr string) *ble.Characteristic {
	// Normaliza o UUID alvo para uma string minúscula sem hifens.
	targetUUID := strings.ToLower(strings.ReplaceAll(uuidStr, "-", ""))

	for _, s := range p.Services {
		for _, c := range s.Characteristics {
			// Normaliza o UUID encontrado da mesma forma.
			foundUUID := strings.ToLower(strings.ReplaceAll(c.UUID.String(), "-", ""))
			// Usa a verificação de string, que é mais tolerante.
			if foundUUID == targetUUID {
				return c
			}
		}
	}
	return nil
}