package proof

func init() { registerBuiltin(func() Preset { return miseTaskPreset{name: "ci", size: SizeLarge} }) }
