package plugin_manager

import (
	"errors"
	"fmt"
	"log"
	"reflect"
	"sync"
	"sync/atomic"
	"time"
)

const PluginTimeout = 100 * time.Millisecond

type PluginStatus int32

const (
	PluginStatusNone PluginStatus = iota
	PluginStatusLoading
	PluginStatusLoaded
	PluginStatusReloading
	PluginStatusUnloading
	PluginStatusUnloaded
)
type Symbol any

type PluginType struct {
	pluginpath string
	err        string        // set if plugin failed to load
	loaded     chan struct{} // closed when loaded
	syms       map[string]any
}

// Open opens a Go plugin.
// If a path has already been opened, then the existing *Plugin is returned.
// It is safe for concurrent use by multiple goroutines.
func Open(path string) (*PluginType, error) {
	return open(path)
}

// Lookup searches for a symbol named symName in plugin p.
// A symbol is any exported variable or function.
// It reports an error if the symbol is not found.
// It is safe for concurrent use by multiple goroutines.
func (p *PluginType) Lookup(symName string) (Symbol, error) {
	return lookup(p, symName)
}

type PluginError struct {
	Type int
	Err  error
}

type PluginFunc func(...interface{}) []interface{}
type pluginFuncInfo struct {
	fn       PluginFunc
	rfv      reflect.Value
	rft      reflect.Type
	inTypes  []reflect.Type
	outTypes []reflect.Type
}

type Plugin struct {
	sync.RWMutex
	m       Manager
	name    string
	version uint64
	path    string
	plugin  *PluginType
	status  PluginStatus
	refs    int
	cache   map[string]*pluginFuncInfo
}

func NewPlugin(path string, m Manager) *Plugin {
	p := &Plugin{
		m:      m,
		path:   path,
		status: PluginStatusNone,
		refs:   0,
		cache:  make(map[string]*pluginFuncInfo),
	}
	return p
}

func (p *Plugin) Status() PluginStatus {
	return PluginStatus(atomic.LoadInt32((*int32)(&(p.status))))
}

func (p *Plugin) setStatus(status PluginStatus) {
	atomic.StoreInt32((*int32)(&(p.status)), int32(status))
}

func (p *Plugin) Name() string {
	return p.name
}

func (p *Plugin) Version() uint64 {
	return p.version
}

func (p *Plugin) Path() string {
	return p.path
}

func (p *Plugin) Load() error {
	p.Lock()
	defer p.Unlock()
	if p.Status() != PluginStatusNone && p.Status() != PluginStatusUnloaded {
		return nil
	}
	p.setStatus(PluginStatusLoading)
	path := p.path
	p1, e := Open(path)
	if e != nil {
		log.Print("load plugin ", path, " error: ", e)
		p.setStatus(PluginStatusNone)
		return e
	}
	p.plugin = p1
	f, e := p1.Lookup("Load")
	if e != nil {
		log.Print("load plugin ", path, " error: ", e)
		p.setStatus(PluginStatusNone)
		return e
	}
	register := func(name string, version uint64) error {
		p.name = name
		p.version = version
		s := fmt.Sprintf("load plugin: %s, version: 0x%x", p.name, p.version)
		p1, e1 := p.m.GetPluginWithVersion(name, version)
		if p1 != nil {
			e1 = errors.New("can't double load plugin")
			log.Println(s, ", error: ", e1.Error())
			p.setStatus(PluginStatusNone)
			return e1
		} else {
			log.Println(s)
			p.setStatus(PluginStatusLoaded)
			p.m.OnLoaded(p)
			return nil
		}
	}
	e = f.(func(func(string, uint64) error) error)(register)

	return e
}

func (p *Plugin) Reload() error {
	if err := p.Unload(); err != nil {
		return err
	}
	if err := p.Load(); err != nil {
		return err
	}
	p.RLock()
	defer p.RUnlock()
	name := p.name
	version := p.version
	s := fmt.Sprintf("reload plugin: %s, version: 0x%x", name, version)
	log.Print(s)
	p.setStatus(PluginStatusLoaded)
	return nil
}

func (p *Plugin) Unload() error {
	p.Lock()
	defer p.Unlock()
	if p.Status() == PluginStatusUnloaded ||
		p.Status() == PluginStatusUnloading ||
		p.Status() == PluginStatusNone {
		return nil
	}
	p.cache = make(map[string]*pluginFuncInfo)
	name := p.name
	version := p.version
	s := fmt.Sprintf("unload plugin: %s, version: 0x%x", name, version)
	f, e := p.plugin.Lookup("Unload")
	if e != nil {
		log.Print(s, ", error: ", e)
		return e
	}
	err := f.(func() error)()
	log.Print(s)
	p.setStatus(PluginStatusUnloaded)
	return err
}

func (p *Plugin) Call(fun string, params ...interface{}) []interface{} {
	f, err := p.GetFunc(fun)
	if err != nil {
		return []interface{}{err}
	}
	return f(params...)
}

func (p *Plugin) GetFunc(fun string) (f func(...interface{}) []interface{}, err error) {
	p.Lock()
	defer p.Unlock()
	if p.plugin == nil {
		err = errors.New("plugin not loaded")
		return
	}
	info, ok := p.cache[fun]
	if ok {
		return info.fn, nil
	}
	f1, err := p.plugin.Lookup(fun)
	if err != nil {
		return nil, err
	}
	info = &pluginFuncInfo{}
	info.rfv = reflect.ValueOf(f1)
	info.rft = reflect.TypeOf(f1)
	li := info.rfv.Type().NumIn()
	lo := info.rfv.Type().NumOut()
	info.inTypes = make([]reflect.Type, li)
	info.outTypes = make([]reflect.Type, lo)
	for i := 0; i < li; i++ {
		info.inTypes[i] = info.rfv.Type().In(i)
	}
	for i := 0; i < lo; i++ {
		info.outTypes[i] = info.rfv.Type().Out(i)
	}
	f = func(params ...interface{}) []interface{} {
		out := make([]interface{}, len(info.outTypes))
		if len(params) != len(info.inTypes) {
			err := errors.New("The number of params is not adapted.")
			out[len(out)-1] = err
			return out
		}
		in := make([]reflect.Value, len(params))
		for k, param := range params {
			in[k] = reflect.ValueOf(param)
			if info.inTypes[k].Name() != in[k].Type().Name() {
				err := fmt.Sprintf("the type of params is not adapted, params[%d] require type %s",
					k, info.inTypes[k].Name())
				err = fmt.Sprintf("failed to call [%s], %s", info.rft.Name(), err)
				log.Println(err)
				out[len(out)-1] = errors.New(err)
				return out
			}
		}
		result := info.rfv.Call(in)
		for i := 0; i < len(result); i++ {
			out[i] = result[i].Interface()
		}

		return out
	}
	info.fn = f
	p.cache[fun] = info
	return f, nil
}