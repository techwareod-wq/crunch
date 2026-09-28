package models

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
)

var DB *mongo.Database

// Connect establishes a MongoDB connection and sets the package-level DB handle.
func Connect(uri, dbName string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	clientOpts := options.Client().ApplyURI(uri)
	client, err := mongo.Connect(ctx, clientOpts)
	if err != nil {
		return fmt.Errorf("mongo connect: %w", err)
	}

	if err := client.Ping(ctx, readpref.Primary()); err != nil {
		return fmt.Errorf("mongo ping: %w", err)
	}

	DB = client.Database(dbName)
	return nil
}

func Collection(name string) *mongo.Collection {
	return DB.Collection(name)
}

func FindOne(ctx context.Context, collection string, filter interface{}, result interface{}) (bool, error) {
	err := Collection(collection).FindOne(ctx, filter).Decode(result)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func InsertOne(ctx context.Context, collection string, doc interface{}) (primitive.ObjectID, error) {
	res, err := Collection(collection).InsertOne(ctx, doc)
	if err != nil {
		return primitive.NilObjectID, err
	}
	return res.InsertedID.(primitive.ObjectID), nil
}

func UpdateOne(ctx context.Context, collection string, filter, update interface{}) error {
	res, err := Collection(collection).UpdateOne(ctx, filter, update)
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("updateOne: no document matched filter in %s", collection)
	}
	return nil
}

func DeleteOne(ctx context.Context, collection string, filter interface{}) error {
	res, err := Collection(collection).DeleteOne(ctx, filter)
	if err != nil {
		return err
	}
	if res.DeletedCount == 0 {
		return fmt.Errorf("deleteOne: no document matched filter in %s", collection)
	}
	return nil
}
