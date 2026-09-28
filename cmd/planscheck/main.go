// Command planscheck is a THROWAWAY read-only debugging probe (2026-08-25
// analytics 402 hunt): dumps the plans catalog's feature lists and a few user
// entitlement projections so the resolver's verdict can be reproduced by hand.
// Delete after use.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/joho/godotenv"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func main() {
	_ = godotenv.Load()
	uri := os.Getenv("MONGO_URI")
	dbName := os.Getenv("MONGO_DB_NAME")
	if uri == "" || dbName == "" {
		fmt.Fprintln(os.Stderr, "MONGO_URI / MONGO_DB_NAME required")
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(uri))
	if err != nil {
		fmt.Fprintln(os.Stderr, "connect:", err)
		os.Exit(1)
	}
	defer client.Disconnect(ctx)
	db := client.Database(dbName)

	fmt.Println("== plans ==")
	cur, err := db.Collection("plans").Find(ctx, bson.M{})
	if err != nil {
		fmt.Fprintln(os.Stderr, "plans find:", err)
		os.Exit(1)
	}
	var plans []bson.M
	if err := cur.All(ctx, &plans); err != nil {
		fmt.Fprintln(os.Stderr, "plans decode:", err)
		os.Exit(1)
	}
	for _, p := range plans {
		fmt.Printf("app=%v tier=%v active=%v\n", p["app_id"], p["tier"], p["active"])
		fmt.Printf("  features:       %v\n", p["features"])
		fmt.Printf("  trial_features: %v\n", p["trial_features"])
		if variants, ok := p["variants"].(bson.M); ok {
			for name, v := range variants {
				if vm, ok := v.(bson.M); ok {
					fmt.Printf("  variant %s price_id=%v\n", name, vm["price_id"])
				}
			}
		}
	}

	email := os.Getenv("CHECK_EMAIL")
	filter := bson.M{"entitlements.indexly": bson.M{"$exists": true}}
	if email != "" {
		filter = bson.M{"email": email}
	}
	fmt.Println("\n== users (entitlement projections, newest 10) ==")
	ucur, err := db.Collection("users").Find(ctx, filter,
		options.Find().SetProjection(bson.M{"email": 1, "entitlements": 1}).
			SetSort(bson.M{"_id": -1}).SetLimit(10))
	if err != nil {
		fmt.Fprintln(os.Stderr, "users find:", err)
		os.Exit(1)
	}
	var users []bson.M
	if err := ucur.All(ctx, &users); err != nil {
		fmt.Fprintln(os.Stderr, "users decode:", err)
		os.Exit(1)
	}
	for _, u := range users {
		fmt.Printf("email=%v entitlements=%v\n", u["email"], u["entitlements"])
	}
}
