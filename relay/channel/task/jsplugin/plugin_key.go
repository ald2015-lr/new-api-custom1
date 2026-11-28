package jsplugin

// PluginKey is the key of the task plugin the adaptor runs. Content
// moderation reads it: it knows the request of Sora only.
func (a *TaskAdaptor) PluginKey() string {
	if a == nil || a.plugin == nil {
		return ""
	}
	return a.plugin.Meta.Key
}
