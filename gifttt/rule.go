package gifttt

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/drtoful/twik"
	"github.com/drtoful/twik/ast"
)

var (
	_manager  *VariableManager
	varPrefix = "var~"
)

type VariableManager struct {
	Updates chan *Value
	cache   map[string]*Value
}

type Value struct {
	Name  string      `json:"-"`
	Value interface{} `json:"value"`
}

func GetManager() *VariableManager {
	if _manager == nil {
		_manager = &VariableManager{
			Updates: make(chan *Value),
			cache:   make(map[string]*Value),
		}
	}
	return _manager
}

func (vm *VariableManager) Get(name string) (interface{}, error) {
	// check cache first
	if v, ok := vm.cache[name]; ok {
		return v.Value, nil
	}

	store := GetStore()
	b, err := store.Get(varPrefix + name)
	if err != nil {
		return nil, fmt.Errorf("undefined symbol: %s", name)
	}

	v := &Value{}
	if err := json.Unmarshal([]byte(b), v); err == nil {
		return v.Value, nil
	} else {
		return nil, err
	}

	panic("never reached")
}

func (vm *VariableManager) Set(name string, value interface{}) error {
	// check if the value has changed since the last time we set it
	old, err := vm.Get(name)
	if err == nil && old == value {
		return nil
	}

	v := &Value{Value: value, Name: name}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}

	store := GetStore()
	vm.cache[name] = v
	vm.Updates <- v
	return store.Set(varPrefix+name, string(b))
}

// the GlobalScope encapsulated over the DefaultScope of the LISP
// interpreter. Get/Set will be delegated to it, so we can answer
// with the data in the VariableManager
type GlobalScope struct {
	fset *ast.FileSet
}

func (s *GlobalScope) Create(symbol string, value interface{}) error {
	panic("never reached")
}

func (s *GlobalScope) Set(symbol string, value interface{}) error {
	manager := GetManager()
	return manager.Set(symbol, value)
}

func (s *GlobalScope) Get(symbol string) (interface{}, error) {
	manager := GetManager()
	return manager.Get(symbol)
}

func (s *GlobalScope) Branch() twik.Scope {
	panic("never reached")
}

func (s *GlobalScope) Eval(node ast.Node) (interface{}, error) {
	scope := twik.NewDefaultScope(s.fset)
	scope.Enclose(s)
	scope.Create("run", runFn)
	scope.Create("log", logFn)
	return scope.Eval(node)
}

func (s *GlobalScope) Enclose(parent twik.Scope) error {
	panic("never reached")
}

// "run" let's the user execute arbitrary commands
func runFn(args []interface{}) (interface{}, error) {
	if len(args) < 1 {
		return nil, errors.New("run takes at least one argument")
	}

	commands := []string{}
	for _, arg := range args {
		if s, ok := arg.(string); ok {
			commands = append(commands, s)
		} else {
			return nil, errors.New("run only takes string arguments")
		}
	}

	var cmd *exec.Cmd
	if len(commands) == 1 {
		cmd = exec.Command(commands[0])
	} else {
		cmd = exec.Command(commands[0], commands[1:]...)
	}

	if err := cmd.Run(); err == nil {
		cmd.Wait()
	}

	return nil, nil
}

// "log" a message
func logFn(args []interface{}) (interface{}, error) {
	if len(args) == 1 {
		if s, ok := args[0].(string); ok {
			log.Println(s)
			return nil, nil
		}
	}
	return nil, errors.New("log function takes a single string argument")
}

func NewGlobalScope(fset *ast.FileSet) twik.Scope {
	scope := &GlobalScope{
		fset: fset,
	}
	return scope
}

type Rule struct {
	Name    string
	program ast.Node
	scope   twik.Scope
}

func NewRule(name string, r io.Reader) (*Rule, error) {
	data, err := ioutil.ReadAll(r)
	if err != nil {
		return nil, err
	}

	fset := twik.NewFileSet()
	scope := NewGlobalScope(fset)

	node, err := twik.Parse(fset, name, data)
	if err != nil {
		return nil, err
	}

	return &Rule{
		Name:    name,
		program: node,
		scope:   scope,
	}, nil
}

func (r *Rule) Run() error {
	_, err := r.scope.Eval(r.program)
	return err
}

type RuleManager struct {
	rules []*Rule
}

func NewRuleManager(path string) *RuleManager {
	manager := &RuleManager{
		rules: []*Rule{},
	}

	files, _ := ioutil.ReadDir(path)
	count := 0
	for _, f := range files {
		if !f.IsDir() && strings.HasSuffix(f.Name(), ".rule") {
			file, err := os.Open(f.Name())
			if err != nil {
				log.Printf("error opening '%s': %s\n", f.Name(), err.Error())
				continue
			}
			defer file.Close()

			rule, err := NewRule(f.Name(), file)
			if err != nil {
				log.Println(err)
				continue
			}

			manager.rules = append(manager.rules, rule)
			count += 1
		}
	}
	log.Printf("loaded %d rules\n", count)

	return manager
}

func (m *RuleManager) Run() {
	vm := GetManager()

	// the rule manager keeps track of time
	go func() {
		ticker := time.NewTicker(time.Second)
		for range ticker.C {
			now := time.Now()
			vm.Set("time:second", int64(now.Second()))
			vm.Set("time:minute", int64(now.Minute()))
			vm.Set("time:hour", int64(now.Hour()))
			vm.Set("date:day", int64(now.Day()))
			vm.Set("date:month", int64(now.Month()))
			vm.Set("date:year", int64(now.Year()))
		}
	}()

	for {
		<-vm.Updates

		// TODO: only run rules, that are affected by a change
		//       to this variable
		go func() {
			for _, r := range m.rules {
				err := r.Run()
				if err != nil {
					log.Printf("error in '%s': %s\n", r.Name, err.Error())
				}
			}
		}()
	}
}
