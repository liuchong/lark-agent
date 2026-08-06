package model

import "fmt"

type ProviderFactory struct {
	profiles map[string]Profile
	bindings RoleBindings
}

func NewProviderFactory(profiles []Profile, bindings RoleBindings) ProviderFactory {
	byName := make(map[string]Profile, len(profiles))
	for _, profile := range profiles {
		if profile.Name == "" {
			continue
		}
		byName[profile.Name] = profile
	}
	return ProviderFactory{
		profiles: byName,
		bindings: bindings,
	}
}

func (f ProviderFactory) Resolve(role Role) (Profile, Codec, error) {
	profileName, ok := f.bindings.profileName(role)
	if !ok {
		return Profile{}, nil, fmt.Errorf("unknown model role %q", role)
	}
	if profileName == "" {
		return Profile{}, nil, fmt.Errorf("model role %q is not bound to a profile", role)
	}
	profile, ok := f.profiles[profileName]
	if !ok {
		return Profile{}, nil, fmt.Errorf("model profile %q for role %q does not exist", profileName, role)
	}
	codec, err := CodecForProfile(profile)
	if err != nil {
		return Profile{}, nil, err
	}
	return profile, codec, nil
}

func CodecForProfile(profile Profile) (Codec, error) {
	switch profile.Protocol {
	case ProtocolOpenAIChat:
		return OpenAIChatCodec{}, nil
	case ProtocolOpenAIResponses:
		return OpenAIResponsesCodec{}, nil
	case ProtocolAnthropicMessages:
		return AnthropicMessagesCodec{}, nil
	default:
		return nil, fmt.Errorf("unsupported model protocol %q for profile %q", profile.Protocol, profile.Name)
	}
}

func (b RoleBindings) profileName(role Role) (string, bool) {
	switch role {
	case RoleAgent:
		return b.Agent, true
	case RoleSemantic:
		return b.Semantic, true
	case RoleFinalizer:
		return b.Finalizer, true
	case RoleCompactor:
		return b.Compactor, true
	case RoleVision:
		return b.Vision, true
	default:
		return "", false
	}
}
