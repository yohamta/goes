package saga

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/modernice/goes/aggregate"
	"github.com/modernice/goes/command"
	"github.com/modernice/goes/event"
	"github.com/modernice/goes/internal/xtime"
	"github.com/modernice/goes/saga/action"
	"github.com/modernice/goes/saga/report"
)

var (
	// ErrActionNotFound is returned when trying to run an Action which is not
	// configured.
	ErrActionNotFound = errors.New("action not found")

	// ErrEmptyName is returned when an Action is configured with an empty name.
	ErrEmptyName = errors.New("empty action name")

	// ErrCompensateTimeout is returned when the compensation of a SAGA fails
	// due to the CompensateTimeout being exceeded.
	ErrCompensateTimeout = errors.New("compensation timed out")
)

var (
	// DefaultCompensateTimeout is the default timeout for compensating Actions
	// of a failed SAGA.
	DefaultCompensateTimeout = 10 * time.Second
)

// Setup is the setup for a SAGA.
type Setup interface {
	// Sequence returns the names of the actions that should be run sequentially.
	Sequence() []string

	// Compensator finds and returns the name of the compensating action for the
	// Action with the given name. If Compensator returns an empty string, there
	// is no compensator for the given action configured.
	Compensator(string) string

	// Action returns the action with the given name. Action returns nil if no
	// Action with that name was configured.
	Action(string) action.Action
}

// A Reporter reports the result of a SAGA.
type Reporter interface {
	Report(report.Report)
}

// Option is a Setup option.
type Option func(*setup)

// ExecutorOption is an option for the Execute function.
type ExecutorOption func(*Executor)

// CompensateErr is returned when the compensation of a failed SAGA fails.
type CompensateErr struct {
	Err         error
	ActionError error
}

// An Executor executes SAGAs. Use NewExector to create an Executor.
type Executor struct {
	Setup

	reporter Reporter
	eventBus event.Bus
	cmdBus   command.Bus
	repo     aggregate.Repository

	skipValidate      bool
	compensateTimeout time.Duration

	sequence []action.Action
	reports  []action.Report
}

type setup struct {
	actions      []action.Action
	actionMap    map[string]action.Action
	sequence     []string
	compensators map[string]string
	startWith    string
}

// Action returns an Option that adds an Action to a SAGA. The first configured
// Action of a SAGA is also its starting Action, unless the StartWith Option is
// used.
func Action(name string, run func(action.Context) error) Option {
	return Add(action.New(name, run))
}

// Add adds Actions to the SAGA.
func Add(acts ...action.Action) Option {
	return func(s *setup) {
		for _, act := range acts {
			if act == nil {
				continue
			}
			s.actions = append(s.actions, act)
		}
	}
}

// StartWith returns an Option that configures the starting Action if a SAGA.
func StartWith(action string) Option {
	return func(s *setup) {
		s.startWith = action
	}
}

// Sequence returns an Option that defines the sequence of Actions that should
// be run when the SAGA is executed.
//
// When the Sequence Option is used, the StartWith Option has no effect and the
// first Action of the sequence is used as the starting Action.
//
// Example:
//
//	s := saga.New(
//		saga.Action("foo", func(action.Context) error { return nil }),
//		saga.Action("bar", func(action.Context) error { return nil }),
//		saga.Action("baz", func(action.Context) error { return nil }),
//		saga.Sequence("bar", "foo", "baz"),
//	)
//	err := saga.Execute(context.TODO(), s)
//	// would run "bar", "foo" & "baz" sequentially
func Sequence(actions ...string) Option {
	return func(s *setup) {
		s.sequence = actions
	}
}

// Compensate returns an Option that configures the compensating Action for a
// failed Action.
func Compensate(failed, compensateWith string) Option {
	return func(s *setup) {
		s.compensators[failed] = compensateWith
	}
}

// Report returns an ExecutorOption that configures the Reporter r to be used by
// the SAGA to report the execution result of the SAGA.
func Report(r Reporter) ExecutorOption {
	return func(e *Executor) {
		e.reporter = r
	}
}

// SkipValidation returns an ExecutorOption that disables validation of a Setup
// before it is executed.
func SkipValidation() ExecutorOption {
	return func(e *Executor) {
		e.skipValidate = true
	}
}

// EventBus returns an ExecutorOption that provides a SAGA with an event.Bus.
// Actions within that SAGA that receive an action.Context may publish events
// through that Context over the provided Bus.
//
// Example:
//
//	s := saga.New(saga.Action("foo", func(ctx action.Context) {
//		evt := event.New("foo", fooData{})
//		err := ctx.Publish(ctx, evt)
//		// handle err
//	}))
//
//	var bus event.Bus
//	err := saga.Execute(context.TODO(), s, saga.EventBus(bus))
//	// handle err
func EventBus(bus event.Bus) ExecutorOption {
	return func(e *Executor) {
		e.eventBus = bus
	}
}

// CommandBus returns an ExecutorOption that provides a SAGA with an command.Bus.
// Actions within that SAGA that receive an action.Context may dispatch Commands
// through that Context over the provided Bus. Dispatches over the Command Bus
// are automatically made synchronous.
//
// Example:
//
//	s := saga.New(saga.Action("foo", func(ctx action.Context) {
//		cmd := command.New("foo", fooPayload{})
//		err := ctx.Dispatch(ctx, cmd)
//		// handle err
//	}))
//
//	var bus command.Bus
//	err := saga.Execute(context.TODO(), s, saga.CommandBus(bus))
//	// handle err
func CommandBus(bus command.Bus) ExecutorOption {
	return func(e *Executor) {
		e.cmdBus = bus
	}
}

// Repository returns an ExecutorOption that provides a SAGA with an
// aggregate.Repository. Action within that SAGA that receive an action.Context
// may fetch aggregates through that Context from the provided Repository.
//
// Example:
//
//	s := saga.New(saga.Action("foo", func(ctx action.Context) {
//		foo := newFooAggregate()
//		err := ctx.Fetch(ctx, foo)
//		// handle err
//	}))
//
//	var repo aggregate.Repository
//	err := saga.Execute(context.TODO(), s, saga.Repository(repo))
//	// handle err
func Repository(r aggregate.Repository) ExecutorOption {
	return func(e *Executor) {
		e.repo = r
	}
}

// CompensateTimeout returns an ExecutorOption that sets the timeout for
// compensating a failed SAGA.
func CompensateTimeout(d time.Duration) ExecutorOption {
	return func(e *Executor) {
		e.compensateTimeout = d
	}
}

// New returns a reusable Setup that can be safely executed concurrently.
//
// Define Actions
//
// The core of a SAGA are Actions. Actions are basically just named functions
// that can be composed to orchestrate the execution flow of a SAGA. Action can
// be configured with the Action Option:
//
//	s := saga.New(
//		saga.Action("foo", func(action.Context) error { return nil })),
//		saga.Action("bar", func(action.Context) error { return nil })),
//	)
//
// By default, the first configured Action is the starting point for the SAGA
// when it's executed. The starting Action can be overriden with the StartWith
// Option:
//
//	s := saga.New(
//		saga.Action("foo", func(action.Context) error { return nil })),
//		saga.Action("bar", func(action.Context) error { return nil })),
//		saga.StartWith("bar"),
//	)
//
// Alternatively define a sequence of Actions that should be run when the SAGA
// is executed. The first Action of the sequence will be used as the starting
// Action for the SAGA:
//
//	s := saga.New(
//		saga.Action("foo", func(action.Context) error { return nil }),
//		saga.Action("bar", func(action.Context) error { return nil }),
//		saga.Action("baz", func(action.Context) error { return nil }),
//		saga.Sequence("bar", "foo", "baz"),
//	)
//	// would run "bar", "foo" & "baz" sequentially
//
// Compensate Actions
//
// Every Action a can be assigned a compensating Action c that is called when
// the SAGA fails. Actions are compensated in reverse order and only failed
// Actions will be compensated.
//
// Example:
//
//	s := saga.New(
//		saga.Action("foo", func(action.Context) error { return nil }),
//		saga.Action("bar", func(action.Context) error { return nil }),
//		saga.Action("baz", func(action.Context) error { return errors.New("whoops") }),
//		saga.Action("compensate-foo", func(action.Context) error { return nil }),
//		saga.Action("compensate-bar", func(action.Context) error { return nil }),
//		saga.Action("compensate-baz", func(action.Context) error { return nil }),
//		saga.Compensate("foo", "compensate-foo"),
//		saga.Compensate("bar", "compensate-bar"),
//		saga.Compensate("baz", "compensate-baz"),
//		saga.Sequence("foo", "bar", "baz"),
//	)
//
// The above Setup would run the following Actions in order:
//	`foo`, `bar`, `baz`, `compensate-bar`, `compensate-foo`
//
// A SAGA that successfully compensated every Action still returns the error
// that triggered the compensation. In order to check if a SAGA was compensated,
// unwrap the error into a *CompensateErr:
//
//	var s saga.Setup
//	if err := saga.Execute(context.TODO(), s); err != nil {
//		if compError, ok := saga.CompensateError(err); ok {
//			log.Println(fmt.Sprintf("Compensation failed: %s", compError))
//		} else {
//			log.Println(fmt.Sprintf("SAGA failed: %s", err))
//		}
//	}
//
// Action Context
//
// An Action has access to an action.Context that allows the Action to run
// other Actions in the same SAGA and, depending on the passed ExecutorOptions,
// publish events and dispatch Commands:
//
//	s := saga.New(
//		saga.Action("foo", func(ctx action.Context) error {
//			if err := ctx.Run(ctx, "bar"); err != nil {
//				return fmt.Errorf("run %q: %w", "bar", err)
//			}
//			if err := ctx.Publish(ctx, event.New(...)); err != nil {
//				return fmt.Errorf("publish event: %w", err)
//			}
//			if err := ctx.Dispatch(ctx, command.New(...)); err != nil {
//				return fmt.Errorf("publish command: %w", err)
//			}
//			return nil
//		}),
//	)
func New(opts ...Option) Setup {
	s := setup{
		actionMap:    make(map[string]action.Action),
		compensators: make(map[string]string),
	}
	for _, opt := range opts {
		opt(&s)
	}
	for _, act := range s.actions {
		s.actionMap[act.Name()] = act
	}
	if s.startWith == "" && len(s.sequence) == 0 && len(s.actions) > 0 && s.actions[0] != nil {
		s.startWith = s.actions[0].Name()
	}
	return &s
}

// Validate validates that a Setup is configured safely and returns an error in
// the following cases:
//	- `ErrEmptyName` if an Action has an empty name (or just whitespace)
//	- `ErrActionNotFound` if the sequence contains an unconfigured Action
//	- `ErrActionNotFound` if an unknown Action is configured as a compensating Action
func Validate(s Setup) error {
	for _, name := range s.Sequence() {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("%q action: %w", name, ErrEmptyName)
		}

		if s.Action(name) == nil {
			return fmt.Errorf("%q action: %w", name, ErrActionNotFound)
		}

		if cname := s.Compensator(name); cname != "" {
			if s.Action(cname) == nil {
				return fmt.Errorf("%q action: %w", cname, ErrActionNotFound)
			}
		}
	}

	return nil
}

// CompensateError unwraps the *CompensateErr from the given error.
func CompensateError(err error) (*CompensateErr, bool) {
	if err == nil {
		return nil, false
	}
	var cerr *CompensateErr
	if !errors.As(err, &cerr) {
		return nil, false
	}
	return cerr, true
}

// Execute executes the given Setup.
//
// Execution error
//
// Execution can fail because of multiple reasons. Execute returns an error in
// the following cases:
//
//	- Setup validation fails (see Validate)
//	- An Action fails and compensation is disabled
//	- An Action and any of the subsequent compensations fail
//
// Compensation is disabled when no compensators are configured.
//
// Skip validation
//
// Validation of the Setup can be skipped by providing the SkipValidation
// ExecutorOption, but should only be used when the Setup s is ensured to be
// valid (e.g. by calling Validate manually beforehand).
//
// Reporting
//
// When a Reporter r is provided using the Report ExecutorOption, Execute will
// call r.Report with a report.Report which contains detailed information about
// the execution of the SAGA (a *report.Report is also a Reporter):
//
//	s := saga.New(...)
//	var r report.Report
//	err := saga.Execute(context.TODO(), s, saga.Report(&r))
//	// err == r.Error()
func Execute(ctx context.Context, s Setup, opts ...ExecutorOption) error {
	return NewExecutor(opts...).Execute(ctx, s)
}

// NewExecutor returns a SAGA executor.
func NewExecutor(opts ...ExecutorOption) *Executor {
	e := Executor{compensateTimeout: DefaultCompensateTimeout}
	for _, opt := range opts {
		opt(&e)
	}
	return &e
}

func (s *setup) Actions() []action.Action {
	acts := make([]action.Action, len(s.actions))
	for i, act := range s.actions {
		acts[i] = act
	}
	return acts
}

func (s *setup) Compensators() map[string]string {
	m := make(map[string]string, len(s.compensators))
	for k, v := range s.compensators {
		m[k] = v
	}
	return m
}

func (s *setup) Compensator(name string) string {
	return s.compensators[name]
}

func (s *setup) Sequence() []string {
	if len(s.actions) == 0 {
		return nil
	}

	if len(s.sequence) == 0 && s.startWith != "" {
		return []string{s.startWith}
	}

	return s.sequence
}

func (s *setup) Action(name string) action.Action {
	return s.actionMap[name]
}

// Execute executes the given SAGA.
func (e *Executor) Execute(ctx context.Context, s Setup) error {
	e = e.clone(s)

	if !e.skipValidate {
		if err := Validate(s); err != nil {
			return fmt.Errorf("validate setup: %w", err)
		}
	}

	start := xtime.Now()

	for _, name := range s.Sequence() {
		act, err := e.action(name)
		if err != nil {
			return e.finish(start, fmt.Errorf("find %q action: %w", name, err))
		}

		if actionError := e.run(ctx, act); actionError != nil {
			if !e.shouldRollback() {
				return e.finish(start, actionError)
			}

			if err := e.rollback(); err != nil {
				return e.finish(start, &CompensateErr{
					Err:         err,
					ActionError: actionError,
				})
			}

			return e.finish(start, actionError)
		}
	}

	return e.finish(start, nil)
}

func (e *Executor) action(name string) (action.Action, error) {
	act := e.Action(name)
	if act == nil {
		return act, ErrActionNotFound
	}
	return act, nil
}

func (e *Executor) finish(start time.Time, err error) error {
	if e.reporter == nil {
		return err
	}

	end := xtime.Now()
	e.reporter.Report(report.New(
		start, end, report.Add(e.reports...),
		report.Error(err),
	))

	return err
}

func (e *Executor) run(ctx context.Context, act action.Action) error {
	actionCtx := e.newActionContext(ctx, act)
	return e.runContext(actionCtx)
}

func (e *Executor) newActionContext(ctx context.Context, act action.Action) action.Context {
	return action.NewContext(
		ctx,
		act,
		action.WithRunner(e.runAction),
		action.WithEventBus(e.eventBus),
		action.WithCommandBus(e.cmdBus),
		action.WithRepository(e.repo),
	)
}

func (e *Executor) runContext(ctx action.Context) error {
	start := xtime.Now()
	act := ctx.Action()
	err := act.Run(ctx)
	end := xtime.Now()
	e.reports = append(e.reports, action.NewReport(
		act,
		start, end,
		action.Error(err),
	))
	return err
}

func (e *Executor) runAction(ctx context.Context, name string) error {
	act, err := e.action(name)
	if err != nil {
		return fmt.Errorf("find %q action: %w", name, err)
	}
	if err = e.run(ctx, act); err != nil {
		return err
	}
	return nil
}

func (e *Executor) shouldRollback() bool {
	if len(e.reports) == 0 {
		return false
	}
	for _, rep := range e.reports {
		if name := e.Compensator(rep.Action.Name()); name != "" {
			return true
		}
	}
	return false
}

func (e *Executor) rollback() error {
	ctx, cancel := context.WithTimeout(context.Background(), e.compensateTimeout)
	defer cancel()

	for i := len(e.reports) - 1; i >= 0; i-- {
		res := e.reports[i]
		if res.Error != nil {
			continue
		}

		var err error
		select {
		case <-ctx.Done():
			return fmt.Errorf("rollback %q Action: %w", res.Action.Name(), ErrCompensateTimeout)
		case err = <-e.rollbackAction(ctx, res):
		}

		if err != nil {
			return fmt.Errorf("rollback %q action: %w", res.Action.Name(), err)
		}
	}
	return nil
}

func (e *Executor) rollbackAction(ctx context.Context, rep action.Report) <-chan error {
	out := make(chan error)
	ret := func(err error) {
		select {
		case <-ctx.Done():
		case out <- err:
		}
	}
	go func() {
		name := e.Compensator(rep.Action.Name())
		if name == "" {
			ret(nil)
			return
		}
		comp := e.Action(name)
		if comp == nil {
			ret(fmt.Errorf("find %q action: %w", name, ErrActionNotFound))
			return
		}
		ret(e.run(ctx, comp))
	}()
	return out
}

func (e Executor) clone(s Setup) *Executor {
	e.Setup = s
	e.sequence = nil
	e.reports = nil
	return &e
}

func (err *CompensateErr) Error() string {
	return err.Err.Error()
}

func (err *CompensateErr) Unwrap() error {
	return err.Err
}
