// Package kids stores each parent's kids with their packed game state and
// answer log, and applies answers.
package kids

import (
	"context"
	"errors"
	"math/rand/v2"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/danilopavk/alphabeter/internal/game"
)

var (
	ErrNotFound    = errors.New("kids: not found")
	ErrConflict    = errors.New("kids: answer is for an outdated symbol")
	ErrInvalidName = errors.New("kids: name must be 1 to 40 characters")
)

const maxNameLen = 40

type Kid struct {
	ID    int64
	Name  string
	State game.State
}

// LoggedAnswer is one entry of a kid's answer history.
type LoggedAnswer struct {
	Seq     uint32
	At      time.Time
	Symbol  game.Symbol
	Correct bool
}

type Service struct {
	pool *pgxpool.Pool
	now  func() time.Time
	rng  game.Rand
}

func NewService(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool, now: time.Now, rng: globalRand{}}
}

// globalRand uses math/rand/v2's top-level source, which is safe for concurrent use.
type globalRand struct{}

func (globalRand) IntN(n int) int { return rand.IntN(n) }

func (s *Service) Create(ctx context.Context, userID int64, name string) (Kid, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > maxNameLen {
		return Kid{}, ErrInvalidName
	}
	kid := Kid{Name: name, State: game.NewState(s.rng)}
	err := s.pool.QueryRow(ctx,
		`INSERT INTO kids (user_id, name, seq, state) VALUES ($1, $2, 0, $3) RETURNING id`,
		userID, name, game.EncodeState(kid.State),
	).Scan(&kid.ID)
	return kid, err
}

func (s *Service) List(ctx context.Context, userID int64) ([]Kid, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, name, seq, state FROM kids WHERE user_id = $1 ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var kids []Kid
	for rows.Next() {
		var (
			kid  Kid
			seq  int32
			blob []byte
		)
		if err := rows.Scan(&kid.ID, &kid.Name, &seq, &blob); err != nil {
			return nil, err
		}
		if kid.State, err = game.DecodeState(uint32(seq), blob); err != nil {
			return nil, err
		}
		kids = append(kids, kid)
	}
	return kids, rows.Err()
}

func (s *Service) Get(ctx context.Context, userID, kidID int64) (Kid, error) {
	kid, _, err := s.load(ctx, s.pool, userID, kidID, "")
	return kid, err
}

func (s *Service) Delete(ctx context.Context, userID, kidID int64) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM kids WHERE id = $1 AND user_id = $2`, kidID, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Answer records whether the kid named the symbol that was on screen when the
// parent saw seq expectedSeq, and reports what the answer did. If the kid has
// moved on since (double tap, stale tab), nothing is written and the current
// kid is returned with ErrConflict.
func (s *Service) Answer(ctx context.Context, userID, kidID int64, expectedSeq uint32, correct bool) (Kid, game.Outcome, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Kid{}, game.Outcome{}, err
	}
	defer tx.Rollback(ctx)

	kid, lastAt, err := s.load(ctx, tx, userID, kidID, "FOR UPDATE")
	if err != nil {
		return Kid{}, game.Outcome{}, err
	}
	if kid.State.Seq != expectedSeq {
		return kid, game.Outcome{}, ErrConflict
	}

	// Whole seconds, so that chunk start + deltas reproduce every timestamp exactly.
	now := s.now().UTC().Truncate(time.Second)
	event := game.Event{Symbol: kid.State.Current, Correct: correct}
	if lastAt != nil && sameUTCDay(*lastAt, now) {
		event.Delta = uint32(max(now.Sub(*lastAt), 0) / time.Second)
	}
	var outcome game.Outcome
	kid.State, outcome = kid.State.AnswerWithOutcome(correct, s.rng)

	if _, err := tx.Exec(ctx,
		`UPDATE kids SET seq = $1, last_answer_at = $2, state = $3 WHERE id = $4`,
		int32(kid.State.Seq), now, game.EncodeState(kid.State), kidID,
	); err != nil {
		return Kid{}, game.Outcome{}, err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO answer_log (kid_id, day, first_seq, started_at, events) VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (kid_id, day) DO UPDATE SET events = answer_log.events || EXCLUDED.events`,
		kidID, utcDay(now), int32(kid.State.Seq), now, game.AppendEvent(nil, event),
	); err != nil {
		return Kid{}, game.Outcome{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Kid{}, game.Outcome{}, err
	}
	return kid, outcome, nil
}

// History returns every answer the kid has given, oldest first.
func (s *Service) History(ctx context.Context, userID, kidID int64) ([]LoggedAnswer, error) {
	if _, err := s.Get(ctx, userID, kidID); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx,
		`SELECT first_seq, started_at, events FROM answer_log WHERE kid_id = $1 ORDER BY first_seq`, kidID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var history []LoggedAnswer
	for rows.Next() {
		var (
			firstSeq int32
			at       time.Time
			blob     []byte
		)
		if err := rows.Scan(&firstSeq, &at, &blob); err != nil {
			return nil, err
		}
		events, err := game.DecodeEvents(blob)
		if err != nil {
			return nil, err
		}
		at = at.UTC()
		for i, e := range events {
			at = at.Add(time.Duration(e.Delta) * time.Second)
			history = append(history, LoggedAnswer{
				Seq: uint32(firstSeq) + uint32(i), At: at, Symbol: e.Symbol, Correct: e.Correct,
			})
		}
	}
	return history, rows.Err()
}

type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func (s *Service) load(ctx context.Context, q querier, userID, kidID int64, lock string) (Kid, *time.Time, error) {
	var (
		kid    = Kid{ID: kidID}
		seq    int32
		lastAt *time.Time
		blob   []byte
	)
	err := q.QueryRow(ctx,
		`SELECT name, seq, last_answer_at, state FROM kids WHERE id = $1 AND user_id = $2 `+lock,
		kidID, userID,
	).Scan(&kid.Name, &seq, &lastAt, &blob)
	if errors.Is(err, pgx.ErrNoRows) {
		return Kid{}, nil, ErrNotFound
	}
	if err != nil {
		return Kid{}, nil, err
	}
	kid.State, err = game.DecodeState(uint32(seq), blob)
	if lastAt != nil {
		utc := lastAt.UTC()
		lastAt = &utc
	}
	return kid, lastAt, err
}

func utcDay(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func sameUTCDay(a, b time.Time) bool { return utcDay(a).Equal(utcDay(b)) }
