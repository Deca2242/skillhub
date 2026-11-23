package cli

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/spf13/cobra"

	"skillhub/internal/app"
	"skillhub/internal/entrypoints/text"
	"skillhub/internal/shared/domain"
)

func (r *runner) skillsShowCmd() *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   text.SkillShowUse,
		Short: text.SkillShowShort,
		Long:  text.SkillShowLong,
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := r.facade(true)
			if err != nil {
				return err
			}
			res, err := r.resolveSkillForShow(a, args[0])
			if err != nil {
				return err
			}
			hist, err := a.History()
			if err != nil {
				return err
			}
			ops := a.Operations(r.ctx, hist)
			activity := app.SkillActivity(hist, ops, res.skill)

			if asJSON {
				return writeJSON(cmd.OutOrStdout(), buildSkillShowJSON(a, res, activity))
			}
			out := text.SkillShow(buildSkillShowData(a, res, activity))
			_, err = fmt.Fprint(cmd.OutOrStdout(), out)
			return err
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, flagJSON)
	return c
}

type resolvedShowSkill struct {
	skill         domain.Skill
	skillhubState string
	jsonState     string
	sightings     []app.Sighting
	parked        *app.ParkedSkill
}

func (r *runner) resolveSkillForShow(a *app.App, ref string) (resolvedShowSkill, error) {
	id, resolveErr := r.resolve(a, ref)
	if resolveErr == nil {
		skills, err := a.Skills(r.ctx)
		if err != nil {
			return resolvedShowSkill{}, err
		}
		if s, ok := findSkillByID(skills, id); ok {
			return r.activeOrHandDisabled(a, s)
		}
	} else {
		var amb *app.AmbiguousError
		if errors.As(resolveErr, &amb) {
			return resolvedShowSkill{}, resolveErr
		}
	}

	if res, ok, err := r.findIgnoredSkill(a, ref, id); err != nil {
		return resolvedShowSkill{}, err
	} else if ok {
		return res, nil
	}

	if res, ok, err := r.findParkedSkill(a, ref); err != nil {
		return resolvedShowSkill{}, err
	} else if ok {
		return res, nil
	}

	if resolveErr != nil {
		return resolvedShowSkill{}, resolveErr
	}
	return resolvedShowSkill{}, fail(msgNotFound, ref)
}

func (r *runner) activeOrHandDisabled(a *app.App, s domain.Skill) (resolvedShowSkill, error) {
	shState := text.TUIStateActive
	jState := "active"
	if s.State == domain.StateDisabled {
		shState = text.TUIStateDisabledHand
		jState = "disabled_by_hand"
	} else if acts, err := a.SkillActions(r.ctx); err == nil {
		if act, ok := acts[s.ID]; ok && act.Off {
			shState = text.TUIStateDisabledHand
			jState = "disabled_by_hand"
		}
	}
	var sgs []app.Sighting
	if allSgs, err := a.Sightings(r.ctx); err == nil {
		sgs = allSgs[s.ID]
	}
	return resolvedShowSkill{
		skill:         s,
		skillhubState: shState,
		jsonState:     jState,
		sightings:     sgs,
	}, nil
}

func (r *runner) findIgnoredSkill(a *app.App, ref, resolvedID string) (resolvedShowSkill, bool, error) {
	ignored, err := a.Ignored(r.ctx)
	if err != nil {
		return resolvedShowSkill{}, false, err
	}
	var matches []domain.Skill
	for _, ig := range ignored {
		if ig.Skill.ID == ref || (resolvedID != "" && ig.Skill.ID == resolvedID) {
			matches = []domain.Skill{ig.Skill}
			break
		}
		if ig.Skill.Name == ref || slices.Contains(ig.Skill.Aliases, ref) {
			matches = append(matches, ig.Skill)
		}
	}
	if len(matches) == 0 {
		return resolvedShowSkill{}, false, nil
	}
	if len(matches) > 1 {
		ids := make([]string, len(matches))
		for i, m := range matches {
			ids[i] = m.ID
		}
		slices.Sort(ids)
		return resolvedShowSkill{}, false, &app.AmbiguousError{Ref: ref, IDs: ids}
	}
	s := matches[0]
	var sgs []app.Sighting
	if allSgs, err := a.Sightings(r.ctx); err == nil {
		sgs = allSgs[s.ID]
	}
	return resolvedShowSkill{
		skill:         s,
		skillhubState: text.TUIStateIgnored,
		jsonState:     "ignored",
		sightings:     sgs,
	}, true, nil
}

func (r *runner) findParkedSkill(a *app.App, ref string) (resolvedShowSkill, bool, error) {
	parked, err := a.Parked()
	if err != nil {
		return resolvedShowSkill{}, false, err
	}
	for i := range parked {
		p := &parked[i]
		if p.Key == ref || domain.LockID(p.Key) == ref {
			s := domain.Skill{
				ID:        domain.LockID(p.Key),
				Name:      p.Key,
				Origin:    domain.Origin{Kind: domain.OriginNone},
				Locations: []domain.Location{{AgentID: domain.StoreAgentID, Path: p.Key}},
			}
			return resolvedShowSkill{
				skill:         s,
				skillhubState: text.TUIStateDisabled,
				jsonState:     "disabled",
				parked:        p,
			}, true, nil
		}
	}
	return resolvedShowSkill{}, false, nil
}

func findSkillByID(skills []domain.Skill, id string) (domain.Skill, bool) {
	for _, s := range skills {
		if s.ID == id {
			return s, true
		}
	}
	return domain.Skill{}, false
}

func buildSkillShowData(a *app.App, res resolvedShowSkill, activity []app.ActivityEntry) text.SkillShowData {
	d := text.SkillShowData{
		Skill:         res.skill,
		SkillhubState: res.skillhubState,
		Sightings:     res.sightings,
		AgentName:     a.AgentName,
		Activity:      activity,
		Now:           a.Now(),
	}
	if res.parked != nil {
		d.Parked = true
		d.Since = res.parked.Since
		d.ReturnTo = res.parked.Harnesses
		return d
	}
	d.AvailableIn = showAvailableIn(a, res.skill, res.sightings)
	return d
}

func showAvailableIn(a *app.App, s domain.Skill, sgs []app.Sighting) string {
	var ids []string
	if len(sgs) > 0 {
		for _, sg := range sgs {
			if !slices.Contains(ids, sg.Harness) {
				ids = append(ids, sg.Harness)
			}
		}
	} else {
		for _, l := range s.Locations {
			if l.AgentID != domain.StoreAgentID && !slices.Contains(ids, l.AgentID) {
				ids = append(ids, l.AgentID)
			}
		}
	}
	if len(ids) == 0 {
		return text.TUIPanelNowhere
	}
	names := make([]string, len(ids))
	for i, id := range ids {
		names[i] = text.SafeLine(a.AgentName(id))
	}
	return text.JoinNames(names)
}

type skillShowJSON struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Aliases       []string          `json:"aliases,omitempty"`
	Description   string            `json:"description,omitempty"`
	SkillhubState string            `json:"skillhub_state"`
	State         string            `json:"state,omitempty"`
	Issues        []string          `json:"issues,omitempty"`
	Origin        domain.Origin     `json:"origin"`
	Sightings     []skillSightJSON  `json:"sightings"`
	Locations     []domain.Location `json:"locations"`
	Activity      []skillActJSON    `json:"activity"`
	DisabledSince *time.Time        `json:"disabled_since,omitempty"`
	ReturnTo      []string          `json:"return_to,omitempty"`
}

type skillSightJSON struct {
	Harness     string `json:"harness"`
	HarnessName string `json:"harness_name"`
	Kind        string `json:"kind"`
	Entry       string `json:"entry"`
}

type skillActJSON struct {
	At        time.Time `json:"at"`
	Action    string    `json:"action"`
	Target    string    `json:"target"`
	V2        bool      `json:"v2"`
	Operation string    `json:"operation,omitempty"`
	Undo      bool      `json:"undo,omitempty"`
}

func buildSkillShowJSON(a *app.App, res resolvedShowSkill, activity []app.ActivityEntry) skillShowJSON {
	s := res.skill
	out := skillShowJSON{
		ID:            s.ID,
		Name:          s.Name,
		Aliases:       s.Aliases,
		Description:   s.Description,
		SkillhubState: res.jsonState,
		State:         string(s.State),
		Origin:        s.Origin,
		Sightings:     make([]skillSightJSON, 0, len(res.sightings)),
		Locations:     make([]domain.Location, 0, len(s.Locations)),
		Activity:      make([]skillActJSON, 0, len(activity)),
	}
	if len(s.Issues) > 0 {
		out.Issues = make([]string, len(s.Issues))
		for i, iss := range s.Issues {
			out.Issues[i] = string(iss)
		}
	}
	for _, sg := range res.sightings {
		out.Sightings = append(out.Sightings, skillSightJSON{
			Harness:     sg.Harness,
			HarnessName: a.AgentName(sg.Harness),
			Kind:        sg.Kind,
			Entry:       sg.Entry,
		})
	}
	if res.parked != nil {
		since := res.parked.Since
		out.DisabledSince = &since
		out.ReturnTo = res.parked.Harnesses
	} else {
		out.Locations = append(out.Locations, s.Locations...)
	}
	for _, e := range activity {
		out.Activity = append(out.Activity, skillActJSON{
			At:        e.At,
			Action:    string(e.Action),
			Target:    e.Target,
			V2:        e.V2,
			Operation: e.Operation.Action,
			Undo:      e.Operation.Undo,
		})
	}
	return out
}
