from datetime import datetime, timedelta
import requests

import pandas as pd
import plotly.graph_objects as go

# API query parameters: `provider`, `location`, `kind`, `from`, `to`, `polled_from`, `polled_to` (RFC3339), `limit`, `offset` |
res = requests.get("https://meteo.johncloud.fr/api/v1/readings", auth=("test", "test"))
data = res.json()
# data[0] =
# {'provider': 'infoclimat', 'provider_type': 'infoclimat', 'location_name': 'castanet-tolosan', 'lead_time_hours': -51.923611111111114, 'polled_at': '2026-11-12T07:40:25Z', 'kind': 'observation', 'forecast_time': '2026-11-10T03:45:00Z', 'temperature_c': 15, 'feels_like_c': None, 'humidity_pct': 92, 'dew_point_c': 13.7, 'pressure_hpa': 1010.4, 'wind_speed_kmh': 0.8, 'wind_gust_kmh': 4.8, 'wind_direction_deg': 158, 'cloud_cover_pct': None, 'precipitation_probability_pct': None, 'snowfall_cm': None, 'visibility_km': None, 'condition': 'unknown', 'condition_text': ''}

df_all = pd.DataFrame(data)
df_all["forecast_time"] = pd.to_datetime(df_all["forecast_time"], utc=True)

observations = df_all[df_all["kind"] == "observation"]
latest_idx = observations.groupby("location_name")["forecast_time"].idxmax()
stations_ft = [
    (row["location_name"], row["forecast_time"].to_pydatetime())
    for _, row in observations.loc[latest_idx][["location_name", "forecast_time"]].iterrows()
]


def analyse_station(station_name: str, forecast_time: datetime):
    print(station_name)
    ft_min = forecast_time - timedelta(minutes=30)
    ft_max = forecast_time + timedelta(minutes=30)
    mask = (
        (df_all["location_name"] == station_name)
        & (df_all["forecast_time"] >= ft_min)
        & (df_all["forecast_time"] < ft_max)
    )
    return df_all[mask].copy()


df = analyse_station(station_name=stations_ft[0][0], forecast_time=stations_ft[0][1])

x_min, x_max = df["lead_time_hours"].min(), df["lead_time_hours"].max()

tick_step = 12
tick_vals = list(range(int(x_min), int(x_max) + 1, tick_step))


def fmt_lead(h: int) -> str:
    d, r = divmod(int(h), 24)
    if d and r:
        return f"{d}d {r}h"
    if d:
        return f"{d}d"
    return f"{r}h"


tick_text = [fmt_lead(v) for v in tick_vals]

MARKERS = ["circle", "square", "diamond", "triangle-up", "cross", "star", "hexagon"]

fig = go.Figure()
marker_idx = 0
for provider, group in df.groupby("provider"):
    group = group.sort_values("lead_time_hours")
    dotted = (group["kind"] == "observation").all()
    if dotted:
        x_vals = [x_min, x_max]
        y_vals = [group["temperature_c"].iloc[0]] * 2
        marker_symbol = "line-ew"
    else:
        x_vals = group["lead_time_hours"]
        y_vals = group["temperature_c"]
        marker_symbol = MARKERS[marker_idx % len(MARKERS)]
        marker_idx += 1
    fig.add_trace(
        go.Scatter(
            x=x_vals,
            y=y_vals,
            mode="lines+markers",
            name=provider,
            line=dict(dash="dot" if dotted else "solid"),
            marker=dict(symbol=marker_symbol),
        )
    )
zero_utc = stations_ft[0][1].strftime("%Y-%m-%d %H:%M UTC")
fig.update_layout(
    xaxis_title=f"Lead time (0 = {zero_utc})", yaxis_title="Temperature (°C)"
)
fig.update_xaxes(
    showgrid=True,
    autorange="reversed",
    tickvals=tick_vals,
    ticktext=tick_text,
)
fig.update_yaxes(showgrid=True)
fig.show()
