package proof

func init() { registerBuiltin(func() Preset { return miseTaskPreset{name: "build", size: SizeMedium} }) }
