// Package all imports every provider implementation so they register themselves.
package all

import (
	_ "github.com/yanndethe/meteocomp/internal/providers/meteofrance"
	_ "github.com/yanndethe/meteocomp/internal/providers/openmeteo"
	_ "github.com/yanndethe/meteocomp/internal/providers/openweathermap"
	_ "github.com/yanndethe/meteocomp/internal/providers/weatherapi"
)
