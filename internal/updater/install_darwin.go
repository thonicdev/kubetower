package updater

import "context"

func init() {
	installers[MacApp] = installApp
	installers[MacTar] = installMacTar
	sweepers[MacApp] = sweepMacApp
	sweepers[MacTar] = func(exe string) { sweepBeside(exe, ".kubetower-update-*") }
}

// installMacTar replaces the binary from the tar.gz's root `kubetower`.
func installMacTar(_ context.Context, plan Plan) error {
	if err := replaceFromTarGz(plan.Installation.Path, plan.File, "kubetower"); err != nil {
		return err
	}
	return startAgainUnlessQuit(plan)
}
