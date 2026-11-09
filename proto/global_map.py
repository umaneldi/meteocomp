import json
from datetime import datetime

import requests
from pydantic import BaseModel


class Forecast(BaseModel):  # type: ignore
    provider: str  # TEXT    NOT NULL,
    provider_type: str  # TEXT    NOT NULL,

    location_name: str  # TEXT    NOT NULL,

    forecast_time: datetime  # TEXT    NOT NULL,  -- UTC start of the covered period
    granularity: str  # TEXT    NOT NULL,  -- hourly | 3h | daily
    lead_time_hours: float  # REAL    NOT NULL,  -- forecast_time - polled_at (negative for observations)
    kind: str  # TEXT    NOT NULL CHECK (kind IN ('forecast', 'observation')) DEFAULT 'forecast',

    temperature_c: float
    temperature_min_c: float
    temperature_max_c: float
    feels_like_c: float
    humidity_pct: float
    dew_point_c: float
    pressure_hpa: float
    wind_speed_kmh: float
    wind_gust_kmh: float
    wind_direction_deg: float
    cloud_cover_pct: float
    precipitation_mm: float
    precipitation_probability_pct: float
    snowfall_cm: float
    uv_index: float
    visibility_km: float

    condition: str  # TEXT    NOT NULL,  -- normalized, see weather.Condition
    condition_text: str  # TEXT,              -- raw provider description

    created_at: (
        datetime  # TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
    )


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

    for d in dat["forecast"]["forecastday"]:
        for c in d["hour"]:
            forecast: Forecast = Forecast(
                provider="weatherapi",
                provider_type="weatherapi",
                location_name="castanet-tolosan",
                forecast_time=datetime.fromisoformat(c["time"]),
                temperature_c=c["temp_c"],
                temperature_min_c=-99999.0,
                temperature_max_c=-99999.0,
                feels_like_c=c["feelslike_c"],
                humidity_pct=c["humidity"],
                dew_point_c=c["dewpoint_c"],
                pressure_hpa=c["pressure_mb"],
                wind_speed_kmh=c["wind_kph"],
                wind_gust_kmh=c["gust_kph"],
                wind_direction_deg=c["wind_degree"],
                cloud_cover_pct=c["cloud"],
                precipitation_mm=c["precip_mm"],
                precipitation_probability_pct=c["chance_of_rain"],
                snowfall_cm=c["snow_cm"],
                uv_index=c["uv"],
                visibility_km=c["vis_km"],
                condition=c["condition"]["text"],
                condition_text=c["condition"]["text"],
                created_at=datetime.now(),
                granularity="hourly",
                lead_time_hours=(
                    datetime.fromisoformat(c["time"]) - datetime.now()
                ).total_seconds()
                / 3600,
                kind="forecast",
            )
            print(forecast)


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

    for c in dat["list"]:
        forecast = Forecast(
            provider="owm",
            provider_type="openweathermap",
            location_name="castanet-tolosan",
            forecast_time=datetime.fromisoformat(c["dt_txt"]),
            granularity="3h",
            lead_time_hours=(
                datetime.fromisoformat(c["dt_txt"]) - datetime.now()
            ).total_seconds()
            / 3600,
            kind="forecast",
            temperature_c=c["main"]["temp"] - 273.15,
            temperature_min_c=c["main"]["temp_min"] - 273.15,
            temperature_max_c=c["main"]["temp_max"] - 273.15,
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
            precipitation_mm=(
                c.get("rain", {}).get("3h")
                if c.get("rain") and c["rain"].get("3h")
                else -99999.0
            ),
            precipitation_probability_pct=-99999.0,
            snowfall_cm=(
                c.get("snow", {}).get("3h")
                if c.get("snow") and c["snow"].get("3h")
                else -99999.0
            ),
            uv_index=-99999.0,
            visibility_km=(
                c.get("visibility") / 1000.0 if c.get("visibility") else -99999.0
            ),
            condition="",
            condition_text=(
                c["weather"][0]["description"]
                if c.get("weather") and c["weather"][0]["description"]
                else ""
            ),
            created_at=datetime.now(),
        )
        print(forecast)


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

    for c in dat["hourly"]["000PU"]:
        forecast = Forecast(
            provider="infoclimat",
            provider_type="infoclimat",
            location_name="castanet-tolosan",
            forecast_time=datetime.fromisoformat(c["dh_utc"]),
            granularity="15min",
            lead_time_hours=(
                datetime.fromisoformat(c["dh_utc"]) - datetime.now()
            ).total_seconds()
            / 3600,
            kind="observation",
            temperature_c=(
                c["temperature"] - 273.15 if c.get("temperature") else -99999.0
            ),
            temperature_min_c=(
                c["temperature_min"] - 273.15 if c.get("temperature_min") else -99999.0
            ),
            temperature_max_c=(
                c["temperature_max"] - 273.15 if c.get("temperature_max") else -99999.0
            ),
            feels_like_c=c["feels_like"] - 273.15 if c.get("feels_like") else -99999.0,
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
            precipitation_mm=c.get("pluie_3h", 0.0) if c.get("pluie_3h") else -99999.0,
            precipitation_probability_pct=-99999.0,
            snowfall_cm=(
                c.get("pluie_neige_3h", 0.0) if c.get("pluie_neige_3h") else -99999.0
            ),
            uv_index=-99999.0,
            visibility_km=(
                c.get("visibilite", 0.0) / 1000.0 if c.get("visibilite") else -99999.0
            ),
            condition="",
            condition_text="",
            created_at=datetime.now(),
        )
        print(forecast)


poll_weatherapi()
poll_openweathermap()
poll_infoclimat()
