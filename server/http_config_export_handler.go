package server

import (
	"errors"
	"net/http"
	"time"

	"github.com/evcc-io/evcc/api/globalconfig"
	"github.com/evcc-io/evcc/core/site"
	"github.com/evcc-io/evcc/util/auth"
	"github.com/evcc-io/evcc/util/config"
	"github.com/evcc-io/evcc/util/redact"
	"go.yaml.in/yaml/v4"
)

// exportDevices converts all devices of a handler to their yaml representation,
// regardless of whether they originate from the yaml config file or the database
func exportDevices[T any](h config.Handler[T]) []config.Named {
	devs := h.Devices()
	res := make([]config.Named, 0, len(devs))
	for _, dev := range devs {
		res = append(res, dev.Config())
	}
	return res
}

// exportSite converts the live site configuration- which may have been
// modified via the UI without a restart- to its yaml representation
func exportSite(site site.API) map[string]any {
	meters := map[string]any{}
	if grid := site.GetGridMeterRef(); grid != "" {
		meters["grid"] = grid
	}
	if pv := site.GetPVMeterRefs(); len(pv) > 0 {
		meters["pv"] = pv
	}
	if battery := site.GetBatteryMeterRefs(); len(battery) > 0 {
		meters["battery"] = battery
	}
	if ext := site.GetExtMeterRefs(); len(ext) > 0 {
		meters["ext"] = ext
	}
	if aux := site.GetAuxMeterRefs(); len(aux) > 0 {
		meters["aux"] = aux
	}
	if consumer := site.GetConsumerMeterRefs(); len(consumer) > 0 {
		meters["consumer"] = consumer
	}

	res := map[string]any{
		"title":  site.GetTitle(),
		"meters": meters,
	}
	if curtail := site.GetCurtailerRefs(); len(curtail) > 0 {
		res["curtailers"] = curtail
	}

	return res
}

// exportConfigHandler returns the effective running configuration as a
// downloadable yaml file. Unlike the raw evcc.yaml file, this combines
// devices and settings regardless of whether they are defined in the yaml
// config file, the database, or both.
func exportConfigHandler(site site.API, conf globalconfig.All, authObject auth.Auth) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireExportAuth(w, r, authObject) {
			return
		}

		hidePrivate := r.URL.Query().Get("private") != "false"

		out := conf

		// drop operational/runtime fields that are not part of the device/site config
		out.Log = ""
		out.SponsorToken = ""
		out.Plant = ""
		out.Telemetry = false
		out.Mcp = false
		out.Metrics = false
		out.Profile = false
		out.Levels = nil
		out.Database = globalconfig.DB{}

		// devices: always read live so UI-added/edited devices are included
		out.Meters = exportDevices(config.Meters())
		out.Chargers = exportDevices(config.Chargers())
		out.Vehicles = exportDevices(config.Vehicles())
		out.Loadpoints = exportDevices(config.Loadpoints())
		out.Circuits = exportDevices(config.Circuits())
		out.Curtailers = exportDevices(config.Curtailers())

		// hems: device-managed config takes precedence over legacy yaml
		if devs := config.Hems().Devices(); len(devs) > 0 {
			named := devs[0].Config()
			out.HEMS = globalconfig.Hems{Type: named.Type, Other: named.Other}
		}

		// site: read live values, may have been changed via the UI without restart
		out.Site = exportSite(site)

		b, err := yaml.Marshal(out)
		if err != nil {
			jsonError(w, http.StatusInternalServerError, err)
			return
		}

		res := string(b)
		if hidePrivate {
			res = redact.String(res)
		}

		filename := "evcc-config-" + time.Now().Format("2006-01-02--15-04") + ".yaml"

		w.Header().Set("Content-Type", "application/x-yaml")
		w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
		_, _ = w.Write([]byte(res))
	}
}

// requireExportAuth guards config export downloads: API key passes;
// session users must additionally supply the admin password.
func requireExportAuth(w http.ResponseWriter, r *http.Request, authObject auth.Auth) bool {
	if authObject.GetAuthMode() == auth.Disabled {
		return true
	}

	if key := apiKeyFromRequest(r); key != "" && authObject.ValidateApiKey(key) {
		return true
	}

	if !authObject.IsAdminPasswordValid(r.Header.Get("X-Admin-Password")) {
		jsonError(w, http.StatusUnauthorized, errors.New("admin password required"))
		return false
	}

	return true
}
