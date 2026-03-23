package scaffold

// WritePubspec is a convenience stub for pubspec.yaml generation.
// The actual pubspec.yaml content is handled by pubspec.yaml.tmpl template rendering.
// Post-scaffold operations like `flutter pub get` are handled by `eden setup`.
func WritePubspec(destDir string, data TemplateData) error {
	return nil
}
