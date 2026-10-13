package proof

func init() { registerBuiltin(func() Preset { return miseTaskPreset{name: "lint", size: SizeMedium} }) }
