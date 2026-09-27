package kids

import (
	"context"
	"errors"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/danilopavk/alphabeter/internal/game"
	"github.com/danilopavk/alphabeter/internal/testutil"
)

func newUser(t *testing.T, pool *pgxpool.Pool, email string) int64 {
	t.Helper()
	var id int64
	err := pool.QueryRow(context.Background(),
		`INSERT INTO users (email, password_hash) VALUES ($1, 'x') RETURNING id`, email).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

type fixture struct {
	svc   *Service
	pool  *pgxpool.Pool
	clock time.Time
	user  int64
}

func setup(t *testing.T) *fixture {
	pool := testutil.Postgres(t)
	f := &fixture{
		pool:  pool,
		clock: time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC),
		user:  newUser(t, pool, "parent@example.com"),
	}
	f.svc = NewService(pool)
	f.svc.now = func() time.Time { return f.clock }
	f.svc.rng = rand.New(rand.NewPCG(1, 1))
	return f
}

func TestCreateInitialisesState(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	kid, err := f.svc.Create(ctx, f.user, "  Mia  ")
	if err != nil {
		t.Fatal(err)
	}
	if kid.Name != "Mia" || kid.State.Seq != 0 || kid.State.Batch != 1 || !kid.State.IsUnlocked(kid.State.Current) {
		t.Errorf("created %+v", kid)
	}
	var size int
	if err := f.pool.QueryRow(ctx, `SELECT length(state) FROM kids WHERE id = $1`, kid.ID).Scan(&size); err != nil {
		t.Fatal(err)
	}
	if size != game.StateSize {
		t.Errorf("stored state is %d bytes, want %d", size, game.StateSize)
	}
	got, err := f.svc.Get(ctx, f.user, kid.ID)
	if err != nil || got != kid {
		t.Errorf("Get = %+v, %v; want %+v", got, err, kid)
	}
}

func TestCreateValidatesName(t *testing.T) {
	f := setup(t)
	for _, name := range []string{"", "   ", "This name is far too long for a toddler, surely"} {
		if _, err := f.svc.Create(context.Background(), f.user, name); !errors.Is(err, ErrInvalidName) {
			t.Errorf("Create(%q) err = %v, want ErrInvalidName", name, err)
		}
	}
}

func TestListAndDelete(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	a, _ := f.svc.Create(ctx, f.user, "Ana")
	b, _ := f.svc.Create(ctx, f.user, "Bo")

	list, err := f.svc.List(ctx, f.user)
	if err != nil || len(list) != 2 || list[0].ID != a.ID || list[1].ID != b.ID {
		t.Fatalf("List = %+v, %v", list, err)
	}
	if err := f.svc.Delete(ctx, f.user, a.ID); err != nil {
		t.Fatal(err)
	}
	if list, _ = f.svc.List(ctx, f.user); len(list) != 1 || list[0].ID != b.ID {
		t.Errorf("after delete: %+v", list)
	}
	if err := f.svc.Delete(ctx, f.user, a.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete err = %v", err)
	}
}

func TestKidsAreScopedToTheirParent(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	kid, _ := f.svc.Create(ctx, f.user, "Mia")
	other := newUser(t, f.pool, "other@example.com")

	if list, _ := f.svc.List(ctx, other); len(list) != 0 {
		t.Errorf("other parent sees %d kids", len(list))
	}
	if _, err := f.svc.Get(ctx, other, kid.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get err = %v", err)
	}
	if _, _, err := f.svc.Answer(ctx, other, kid.ID, 0, true); !errors.Is(err, ErrNotFound) {
		t.Errorf("Answer err = %v", err)
	}
	if _, err := f.svc.History(ctx, other, kid.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("History err = %v", err)
	}
	if err := f.svc.Delete(ctx, other, kid.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("Delete err = %v", err)
	}
}

func TestAnswerPersistsStateAndLog(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	kid, _ := f.svc.Create(ctx, f.user, "Mia")
	shown := kid.State.Current

	after, outcome, err := f.svc.Answer(ctx, f.user, kid.ID, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if after.State.Seq != 1 || after.State.Symbols[shown].Streak != 1 {
		t.Errorf("after answer: %+v", after.State)
	}
	wantOutcome := game.Outcome{Symbol: shown, Correct: true, Before: game.StartScore, After: game.StartScore + 5}
	if outcome != wantOutcome {
		t.Errorf("outcome %+v, want %+v", outcome, wantOutcome)
	}
	if got, _ := f.svc.Get(ctx, f.user, kid.ID); got != after {
		t.Errorf("stored state differs from returned state")
	}

	history, err := f.svc.History(ctx, f.user, kid.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := []LoggedAnswer{{Seq: 1, At: f.clock, Symbol: shown, Correct: true}}
	if len(history) != 1 || history[0] != want[0] {
		t.Errorf("history = %+v, want %+v", history, want)
	}
}

func TestAnswerWithStaleSeqChangesNothing(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	kid, _ := f.svc.Create(ctx, f.user, "Mia")
	first, _, _ := f.svc.Answer(ctx, f.user, kid.ID, 0, true)

	again, outcome, err := f.svc.Answer(ctx, f.user, kid.ID, 0, true) // double tap
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	if outcome != (game.Outcome{}) {
		t.Errorf("conflict reported an outcome: %+v", outcome)
	}
	if again != first {
		t.Errorf("conflict should return the current kid")
	}
	if history, _ := f.svc.History(ctx, f.user, kid.ID); len(history) != 1 {
		t.Errorf("history has %d answers, want 1", len(history))
	}
}

func TestConcurrentDoubleTapCountsOnce(t *testing.T) {
	f := setup(t)
	f.svc.rng = globalRand{} // the seeded source is not safe for concurrent use
	ctx := context.Background()
	kid, _ := f.svc.Create(ctx, f.user, "Mia")

	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, errs[i] = f.svc.Answer(ctx, f.user, kid.ID, 0, true)
		}()
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case !errors.Is(err, ErrConflict):
			t.Errorf("unexpected error: %v", err)
		}
	}
	if ok != 1 {
		t.Errorf("%d answers accepted, want 1", ok)
	}
}

func TestLogChunksByUTCDayWithSecondDeltas(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.clock = time.Date(2026, 9, 26, 23, 59, 50, 400_000_000, time.UTC)
	kid, _ := f.svc.Create(ctx, f.user, "Mia")

	times := []time.Time{
		f.clock,                       // 23:59:50 → first chunk
		f.clock.Add(5 * time.Second),  // 23:59:55, delta 5
		f.clock.Add(15 * time.Second), // 00:00:05 next day → new chunk
		f.clock.Add(200 * time.Second),
	}
	for i, at := range times {
		f.clock = at
		if _, _, err := f.svc.Answer(ctx, f.user, kid.ID, uint32(i), i%2 == 0); err != nil {
			t.Fatal(err)
		}
	}

	rows, err := f.pool.Query(ctx, `SELECT day::text, first_seq, length(events) FROM answer_log WHERE kid_id = $1 ORDER BY day`, kid.ID)
	if err != nil {
		t.Fatal(err)
	}
	type chunk struct {
		day            string
		firstSeq, size int
	}
	var chunks []chunk
	for rows.Next() {
		var c chunk
		if err := rows.Scan(&c.day, &c.firstSeq, &c.size); err != nil {
			t.Fatal(err)
		}
		chunks = append(chunks, c)
	}
	want := []chunk{{"2026-09-26", 1, 4}, {"2026-09-27", 3, 5}} // 2+2 bytes; 2+3 bytes (delta 185)
	if len(chunks) != len(want) || chunks[0] != want[0] || chunks[1] != want[1] {
		t.Errorf("chunks = %+v, want %+v", chunks, want)
	}

	history, _ := f.svc.History(ctx, f.user, kid.ID)
	for i, h := range history {
		if wantAt := times[i].Truncate(time.Second); !h.At.Equal(wantAt) || h.Seq != uint32(i+1) {
			t.Errorf("answer %d: seq=%d at=%s, want seq=%d at=%s", i, h.Seq, h.At, i+1, wantAt)
		}
	}
}

func TestHistoryReplaysToStoredState(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	kid, _ := f.svc.Create(ctx, f.user, "Mia")
	rng := rand.New(rand.NewPCG(9, 9))
	for i := range 400 {
		f.clock = f.clock.Add(time.Duration(rng.IntN(3*3600)) * time.Second)
		if _, _, err := f.svc.Answer(ctx, f.user, kid.ID, uint32(i), rng.IntN(4) != 0); err != nil {
			t.Fatal(err)
		}
	}
	stored, _ := f.svc.Get(ctx, f.user, kid.ID)
	history, err := f.svc.History(ctx, f.user, kid.ID)
	if err != nil {
		t.Fatal(err)
	}
	events := make([]game.Event, len(history))
	for i, h := range history {
		if h.Seq != uint32(i+1) {
			t.Fatalf("answer %d has seq %d", i, h.Seq)
		}
		events[i] = game.Event{Symbol: h.Symbol, Correct: h.Correct}
	}
	replayed := game.Replay(events)
	replayed.Current = stored.State.Current
	if replayed != stored.State {
		t.Errorf("replayed state differs from stored state")
	}
}
