package plugin_manager

var defaultManager Manager

func StartManager(options ManagerOptions) error {
	if defaultManager != nil && defaultManager.IsRunning() {
		return nil
	}
	var err error
	defaultManager, err = NewManager(options)
	if err != nil {
		return err
	}
	return defaultManager.Run()
}

func Call(module, function string, args ...interface{}) []interface{} {
	f, err := GetFunc(module, function)
	if err != nil {
		return []interface{}{err}
	}

	return f(args...)
}

func GetPlugin(name string) (*Plugin, error) {
	return defaultManager.GetPlugin(name)
}

func GetPluginWithVersion(name string, version uint64) (*Plugin, error) {
	return defaultManager.GetPluginWithVersion(name, version)
}

func GetFunc(module, function string) (f func(...interface{}) []interface{}, err error) {
	return defaultManager.GetFunc(module, function)
}