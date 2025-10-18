// Local: pkg/database/database.go
package database

import (
	"context"
	"log"
	"os"
	"time"

	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// DB é a nossa conexão global com o banco de dados
var DB *mongo.Client

// InitDB inicializa a conexão com o MongoDB Atlas
func InitDB() *mongo.Client {
	uri := os.Getenv("MONGODB_URI")
	if uri == "" {
		log.Fatal("Erro: MONGODB_URI não foi definida nas variáveis de ambiente.")
	}

	log.Println("[DB] Conectando ao MongoDB Atlas...")
	
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := mongo.Connect(ctx, options.Client().ApplyURI(uri))
	if err != nil {
		log.Fatalf("Falha ao conectar ao MongoDB: %v", err)
	}

	// Testa a conexão
	if err := client.Ping(ctx, nil); err != nil {
		log.Fatalf("❌ Falha ao pingar MongoDB: %v", err)
	}

	DB = client
	log.Println("✅ Conectado ao MongoDB com sucesso!")
	return DB
}