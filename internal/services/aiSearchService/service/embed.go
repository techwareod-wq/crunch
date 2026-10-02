package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/pipeline"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
	"github.com/atharva-ng/crunch/internal/services/aiSearchService"
	"github.com/atharva-ng/crunch/internal/util/log"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

// reembedPage is how many listings one reembed-all page dispatches.
const reembedPage = 500

// Embed (re)embeds one live listing (D-083): build the summary text, skip
// when its hash (model + text) is unchanged, else embed as a document and
// store it, guarded by the live version.
func (s *svc) Embed(ctx context.Context, p aiSearchService.EmbedPayload) error {
	if !s.cfg().Enabled {
		return nil // feature off: a job queued before the switch does nothing
	}
	if s.embedder == nil {
		log.Info("aisearch: embed skipped, no embedder configured", "warehouse", p.WarehouseID)
		return nil
	}
	id, err := primitive.ObjectIDFromHex(p.WarehouseID)
	if err != nil {
		return fmt.Errorf("aisearch.embed: bad id %q: %w", p.WarehouseID, pipeline.ErrPermanent)
	}
	w, err := s.store.GetWarehouse(ctx, id)
	if errors.Is(err, models.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("aisearch.embed: load: %w", err)
	}
	if w.Status != models.WarehouseLive || w.Live == nil || w.LiveVersion != p.LiveVersion {
		return nil // archived, or a newer version has its own job
	}
	text := summaryText(s.rules.Snapshot(), w)
	hash := embedHash(s.embedder.Model(), text)
	if hash == w.EmbeddingHash {
		return nil
	}
	vecs, err := s.embedder.Embed(ctx, []string{text}, interfaces.EmbedDocument)
	if err != nil {
		return fmt.Errorf("aisearch.embed: %w", err)
	}
	if _, err := s.store.SetEmbedding(ctx, id, p.LiveVersion, vecs[0], hash); err != nil {
		return fmt.Errorf("aisearch.embed: store: %w", err)
	}
	return nil
}

// embedHash keys the vector to the model and the text: a model change
// re-embeds on the next run.
func embedHash(model, text string) string {
	sum := sha256.Sum256([]byte(model + "\n" + text))
	return hex.EncodeToString(sum[:])
}

// summaryText is what a listing's vector means (D-083): name, description,
// locality, the public features it has, the industries it suits, total
// area.
func summaryText(snap *domain.Snapshot, w *models.Warehouse) string {
	a := w.Live.Attributes
	res := domain.Evaluate(snap, a, w.UpdatedAt)
	var lines []string
	add := func(s string) {
		if s = strings.TrimSpace(s); s != "" {
			lines = append(lines, s)
		}
	}
	add(w.Name)
	if fv := a.Value(domain.RootKey, "description"); fv != nil {
		if d, ok := domain.DecodeValue[string](fv.V); ok {
			add(d)
		}
	}
	add(strings.Trim(strings.Join([]string{w.Locality, w.City}, ", "), ", "))

	var features []string
	for i := range snap.Nodes {
		n := &snap.Nodes[i]
		if n.Key == domain.RootKey || !n.Public || res.State[n.Key] != domain.StatusYes {
			continue
		}
		features = append(features, n.Name)
		for j := range n.Fields {
			f := &n.Fields[j]
			fv := a.Value(n.Key, f.Key)
			if !f.Public || fv == nil || fv.V == nil {
				continue
			}
			switch f.Type {
			case domain.TypeBool:
				if b, ok := domain.DecodeValue[bool](fv.V); ok && b {
					features = append(features, f.Name)
				}
			case domain.TypePick:
				if k, ok := domain.DecodeValue[string](fv.V); ok {
					features = append(features, optionLabel(f, k))
				}
			case domain.TypeMulti:
				if ks, ok := domain.AsStrings(fv.V); ok {
					for _, k := range ks {
						features = append(features, optionLabel(f, k))
					}
				}
			}
		}
	}
	if len(features) > 0 {
		add("Features: " + strings.Join(features, ", "))
	}
	var suits []string
	for i := range snap.Industries {
		ind := &snap.Industries[i]
		if v := res.Fit[ind.Key]; v == domain.VerdictFit || v == domain.VerdictPartial {
			suits = append(suits, ind.Name)
		}
	}
	if len(suits) > 0 {
		add("Suits: " + strings.Join(suits, ", "))
	}
	if w.TotalSqm > 0 {
		add("Total area: " + strconv.FormatFloat(w.TotalSqm, 'f', 0, 64) + " sq m")
	}
	return strings.Join(lines, "\n")
}

func optionLabel(f *models.AttributeField, key string) string {
	for _, o := range f.Options {
		if o.Key == key {
			return o.Label
		}
	}
	return key
}

// StartReembedAll queues a backfill run (superuser).
func (s *svc) StartReembedAll(ctx context.Context) (string, error) {
	run := strconv.FormatInt(s.now().UnixNano(), 36)
	if err := s.dispatch(ctx, aiSearchService.ProcessReembedAll, "reembed_all:"+run, aiSearchService.ReembedAllPayload{Run: run}); err != nil {
		return "", err
	}
	return run, nil
}

// ReembedAll dispatches an embed job per live listing. Unchanged listings
// are skipped by the hash check inside each job.
func (s *svc) ReembedAll(ctx context.Context, p aiSearchService.ReembedAllPayload) error {
	if !s.cfg().Enabled {
		return nil
	}
	var after primitive.ObjectID
	total := 0
	for {
		refs, err := s.store.LiveRefs(ctx, after, reembedPage)
		if err != nil {
			return fmt.Errorf("aisearch.reembed_all: page: %w", err)
		}
		for _, r := range refs {
			id := r.ID.Hex()
			if err := s.dispatch(ctx, aiSearchService.ProcessEmbed, aiSearchService.EmbedKey(id, r.LiveVersion, p.Run),
				aiSearchService.EmbedPayload{WarehouseID: id, LiveVersion: r.LiveVersion}); err != nil {
				return fmt.Errorf("aisearch.reembed_all: dispatch %s: %w", id, err)
			}
		}
		total += len(refs)
		if len(refs) < reembedPage {
			break
		}
		after = refs[len(refs)-1].ID
	}
	log.Info("aisearch: reembed_all dispatched", "run", p.Run, "warehouses", total)
	return nil
}
