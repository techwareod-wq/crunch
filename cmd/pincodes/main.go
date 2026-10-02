// Command pincodes loads the GeoNames India postal-code dump into the
// `pincodes` collection: the last-resort geocoder for search (D-078).
//
// Download IN.zip from https://download.geonames.org/export/zip/ and unzip
// it. Rows sharing a postal code are merged into one centroid. Defaults to
// a dry run; pass -apply to write. Uses MONGO_URI / MONGO_DB_NAME like the
// service (a .env file is loaded if present). Re-running is safe (upsert).
//
//	go run ./cmd/pincodes -file IN.txt          # dry run: parse + counts
//	go run ./cmd/pincodes -file IN.txt -apply   # write
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/joho/godotenv"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/models"
)

const batchSize = 1000

func main() {
	file := flag.String("file", "", "path to the GeoNames IN.txt dump")
	apply := flag.Bool("apply", false, "write the pincodes (default is a dry run)")
	flag.Parse()
	if *file == "" {
		fail("-file <IN.txt> is required")
	}
	f, err := os.Open(*file)
	if err != nil {
		fail("open: %v", err)
	}
	defer f.Close()
	pins, skipped, err := parse(f)
	if err != nil {
		fail("parse: %v", err)
	}
	fmt.Printf("pincodes: parsed %d postal codes (%d bad rows skipped)\n", len(pins), skipped)
	if !*apply {
		fmt.Println("dry run — re-run with -apply to write")
		return
	}

	_ = godotenv.Load()
	var cfg config.AppConfig
	cfg.LoadEnvConfig()
	if err := config.LoadValues(&cfg); err != nil {
		fail("load values: %v", err)
	}
	cfg.LoadDatabaseConfig()
	if cfg.Database.URL == "" {
		fail("MONGO_URI is not set")
	}
	if err := models.Connect(cfg.Database.URL, cfg.Database.Name); err != nil {
		fail("connect to mongo: %v", err)
	}
	ctx := context.Background()
	if err := models.EnsurePincodeIndexes(ctx); err != nil {
		fail("%v", err)
	}
	var written int64
	for i := 0; i < len(pins); i += batchSize {
		n, err := models.UpsertPincodes(ctx, pins[i:min(i+batchSize, len(pins))])
		if err != nil {
			fail("write batch at %d: %v", i, err)
		}
		written += n
	}
	fmt.Printf("pincodes: wrote %d (db=%s)\n", written, cfg.Database.Name)
}

// parse reads GeoNames postal rows (tab-separated: country, postal code,
// place, state, state code, district, district code, admin3, admin3 code,
// lat, lng, accuracy) and merges rows per postal code: centroid of the
// rows, first place name, all place names lower-cased for lookups.
func parse(r io.Reader) ([]models.Pincode, int, error) {
	type acc struct {
		p      models.Pincode
		n      int
		latSum float64
		lngSum float64
	}
	byCode := map[string]*acc{}
	var order []string
	skipped := 0
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		cols := strings.Split(sc.Text(), "\t")
		if len(cols) < 11 {
			skipped++
			continue
		}
		code := strings.TrimSpace(cols[1])
		lat, err1 := strconv.ParseFloat(strings.TrimSpace(cols[9]), 64)
		lng, err2 := strconv.ParseFloat(strings.TrimSpace(cols[10]), 64)
		if code == "" || err1 != nil || err2 != nil || (lat == 0 && lng == 0) {
			skipped++
			continue
		}
		place, state, district := strings.TrimSpace(cols[2]), strings.TrimSpace(cols[3]), strings.TrimSpace(cols[5])
		a, ok := byCode[code]
		if !ok {
			a = &acc{p: models.Pincode{Code: code, Place: place, District: district, State: state,
				DistrictLC: strings.ToLower(district), Places: []string{}}}
			byCode[code] = a
			order = append(order, code)
		}
		a.n++
		a.latSum += lat
		a.lngSum += lng
		if lc := strings.ToLower(place); lc != "" && !slices.Contains(a.p.Places, lc) {
			a.p.Places = append(a.p.Places, lc)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, skipped, err
	}
	out := make([]models.Pincode, 0, len(order))
	for _, c := range order {
		a := byCode[c]
		a.p.Lat, a.p.Lng = a.latSum/float64(a.n), a.lngSum/float64(a.n)
		out = append(out, a.p)
	}
	return out, skipped, nil
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "pincodes: "+format+"\n", args...)
	os.Exit(1)
}
