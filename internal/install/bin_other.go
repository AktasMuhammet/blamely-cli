//go:build !darwin

package install

// prepareInstalledBinary is a no-op off macOS. Linux has no Gatekeeper, and
// Windows trust (SmartScreen / Mark-of-the-Web) isn't fixable by re-signing an
// unsigned binary, so there's nothing to self-heal here.
func prepareInstalledBinary(path string) error {
	return nil
}
