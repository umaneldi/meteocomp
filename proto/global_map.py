import json
from datetime import datetime

import requests
from pydantic import BaseModel


class Reading(BaseModel):
    provider: str
    provider_type: str  # Identification of the provider type (e.g., "weatherapi", "openweathermap") to use the right API call

    location_name: (
        str  # Name of the location, linked to the locations table (column name)
    )

    forecast_time: datetime  # UTC datetime of the forecast
    lead_time_hours: float  # forecast_time - polled_at (negative for observations)
    kind: str  # Type of the forecast (e.g., "forecast", "observation")

    temperature_c: float
    feels_like_c: float
    humidity_pct: float
    dew_point_c: float
    pressure_hpa: float
    wind_speed_kmh: float
    wind_gust_kmh: float
    wind_direction_deg: float
    cloud_cover_pct: float
    precipitation_probability_pct: float
    snowfall_cm: float
    visibility_km: float

    condition: str
    condition_text: str

    polled_at: datetime  # UTC timestamp when the forecast was created


def poll_weatherapi():
    res = requests.get(
        "http://api.weatherapi.com/v1/forecast.json",
        params={
            "key": "b55668ef36f5492b8a5151801260211",
            "q": "castanet-tolosan",
            "aqi": "no",
            "alerts": "no",
            "days": 7,
        },
    )
    dat = res.json()
    with open("weatherapi_current.json", "w") as f:
        json.dump(dat, f, indent=4)

    ret_forecast = None
    polled_at = datetime.now()
    dat["forecast"]["forecastday"]
    for d in dat["forecast"]["forecastday"]:
        for c in d["hour"]:
            forecast: Reading = Reading(
                provider="weatherapi",
                provider_type="weatherapi",
                location_name="castanet-tolosan",
                forecast_time=datetime.fromisoformat(c["time"]),
                temperature_c=c["temp_c"],
                feels_like_c=c["feelslike_c"],
                humidity_pct=c["humidity"],
                dew_point_c=c["dewpoint_c"],
                pressure_hpa=c["pressure_mb"],
                wind_speed_kmh=c["wind_kph"],
                wind_gust_kmh=c["gust_kph"],
                wind_direction_deg=c["wind_degree"],
                cloud_cover_pct=c["cloud"],
                precipitation_probability_pct=c["chance_of_rain"],
                snowfall_cm=c["snow_cm"],
                visibility_km=c["vis_km"],
                condition=c["condition"]["text"],
                condition_text=c["condition"]["text"],
                polled_at=polled_at,
                lead_time_hours=(
                    datetime.fromisoformat(c["time"]) - polled_at
                ).total_seconds()
                / 3600,
                kind="forecast",
            )
            ret_forecast = forecast

    return ret_forecast


def poll_openweathermap():
    res = requests.get(
        "https://api.openweathermap.org/data/2.5/forecast",
        params={
            "lat": "43.5167",
            "lon": "1.5",
            "cnt": "7",
            "appid": "8775c732c62b9a48c8c8cb5a39ae5f61",
        },
    )
    dat = res.json()
    with open("openweathermap_forecast.json", "w") as f:
        json.dump(dat, f, indent=4)

    ret_forecast = None
    polled_at = datetime.now()
    for c in dat["list"]:
        forecast = Reading(
            provider="owm",
            provider_type="openweathermap",
            location_name="castanet-tolosan",
            forecast_time=datetime.fromisoformat(c["dt_txt"]),
            lead_time_hours=(
                datetime.fromisoformat(c["dt_txt"]) - polled_at
            ).total_seconds()
            / 3600,
            kind="forecast",
            temperature_c=c["main"]["temp"] - 273.15,
            feels_like_c=c["main"]["feels_like"] - 273.15,
            humidity_pct=c["main"]["humidity"],
            dew_point_c=-99999.0,
            pressure_hpa=c["main"]["pressure"],
            wind_speed_kmh=c["wind"]["speed"] * 3.6,
            wind_gust_kmh=(
                c["wind"].get("gust") * 3.6
                if c.get("wind") and c["wind"].get("gust")
                else -99999.0
            ),
            wind_direction_deg=c["wind"]["deg"],
            cloud_cover_pct=c["clouds"]["all"],
            precipitation_probability_pct=-99999.0,
            snowfall_cm=(
                c.get("snow", {}).get("3h")
                if c.get("snow") and c["snow"].get("3h")
                else -99999.0
            ),
            visibility_km=(
                c.get("visibility") / 1000.0 if c.get("visibility") else -99999.0
            ),
            condition="",
            condition_text=(
                c["weather"][0]["description"]
                if c.get("weather") and c["weather"][0]["description"]
                else ""
            ),
            polled_at=polled_at,
        )
        ret_forecast = forecast

    return ret_forecast


def poll_infoclimat():
    res = requests.get(
        "https://www.infoclimat.fr/opendata/",
        params={
            "version": "2",
            "method": "get",
            "format": "json",
            "stations[]": "000PU",
            "start": "2026-11-09",
            "end": "2026-11-09",
            "token": "NQMCcJA55Aiydb0RdimRZLGo37wa95rnhU7Fzx4jKVN48MWzCYRYA",
        },
    )
    dat = res.json()
    with open("infoclimat.json", "w") as f:
        json.dump(dat, f, indent=4)

    ret_forecast = None
    polled_at = datetime.now()
    for c in dat["hourly"]["000PU"]:
        forecast = Reading(
            provider="infoclimat",
            provider_type="infoclimat",
            location_name="castanet-tolosan",
            forecast_time=datetime.fromisoformat(c["dh_utc"]),
            lead_time_hours=(
                datetime.fromisoformat(c["dh_utc"]) - polled_at
            ).total_seconds()
            / 3600,
            kind="observation",
            temperature_c=(c["temperature"] if c.get("temperature") else -99999.0),
            feels_like_c=c["feels_like"] if c.get("feels_like") else -99999.0,
            humidity_pct=c["humidite"],
            dew_point_c=-99999.0,
            wind_direction_deg=(
                c["vent_direction"] if c.get("vent_direction") else -99999.0
            ),
            pressure_hpa=c["pression"] if c.get("pression") else -99999.0,
            wind_speed_kmh=c["vent_moyen"] * 3.6 if c.get("vent_moyen") else -99999.0,
            wind_gust_kmh=(
                c.get("vent_rafales_10min", 0.0) * 3.6
                if c.get("vent_rafales_10min")
                else -99999.0
            ),
            cloud_cover_pct=-99999.0,
            precipitation_probability_pct=-99999.0,
            snowfall_cm=(
                c.get("pluie_neige_3h", 0.0) if c.get("pluie_neige_3h") else -99999.0
            ),
            visibility_km=(
                c.get("visibilite", 0.0) / 1000.0 if c.get("visibilite") else -99999.0
            ),
            condition="",
            condition_text="",
            polled_at=polled_at,
        )
        ret_forecast = forecast

    return ret_forecast


def poll_openmeteo():
    vars = [
        "temperature_2m",
        "apparent_temperature",
        "relative_humidity_2m",
        "dew_point_2m",
        "surface_pressure",
        "wind_gusts_10m",
        "wind_direction_10m",
        "wind_speed_10m",
        "cloud_cover",
        "precipitation_probability",
        "snowfall",
        "weather_code",
    ]
    res = requests.get(
        "https://api.open-meteo.com/v1/forecast",
        params={
            "latitude": "43.5167",
            "longitude": "1.5",
            "hourly": ",".join(vars),
            "models": "ecmwf_ifs",
            "forecast_days": "10",
        },
    )
    dat = res.json()
    with open("openmeteo.json", "w") as f:
        json.dump(dat, f, indent=4)

    ret_forecast = None
    for (
        temperature_2m,
        apparent_temperature,
        relative_humidity_2m,
        dew_point_2m,
        surface_pressure,
        wind_gusts_10m,
        wind_direction_10m,
        wind_speed_10m,
        cloud_cover,
        precipitation_probability,
        snowfall,
        weather_code,
        time,
    ) in zip(*[dat["hourly"][v] for v in vars], dat["hourly"]["time"]):
        forecast: Reading = Reading(
            provider="openmeteo",
            provider_type="openmeteo",
            location_name="castanet-tolosan",
            forecast_time=datetime.fromisoformat(time),
            lead_time_hours=(
                datetime.fromisoformat(time) - datetime.now()
            ).total_seconds()
            / 3600,
            kind="forecast",
            temperature_c=temperature_2m,
            feels_like_c=apparent_temperature,
            humidity_pct=relative_humidity_2m,
            dew_point_c=dew_point_2m,
            wind_direction_deg=wind_direction_10m,
            pressure_hpa=surface_pressure,
            wind_speed_kmh=wind_speed_10m * 3.6,
            wind_gust_kmh=wind_gusts_10m * 3.6,
            cloud_cover_pct=cloud_cover,
            precipitation_probability_pct=precipitation_probability,
            snowfall_cm=snowfall,
            visibility_km=-99999.0,
            condition="",
            condition_text="",
            polled_at=datetime.now(),
        )
        ret_forecast = forecast

    return ret_forecast


f = poll_weatherapi()
print(f)

f = poll_openweathermap()
print(f)

f = poll_infoclimat()
print(f)

f = poll_openmeteo()
print(f)
