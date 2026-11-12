from datetime import datetime, timedelta, timezone, UTC

import pandas as pd
import plotly.graph_objects as go
import requests


def get_by_kind(
    kind: str,
    forecast_timedelta: int = 90,
    forecast_time: None | datetime = None,
):
    # API query parameters: `provider`, `location`, `kind`, `from`, `to`, `polled_from`, `polled_to`, `forecast_time` (RFC3339), `forecast_timedelta` (minutes, default 30), `limit`, `offset`
    # data[0] =
    # {'provider': 'infoclimat', 'provider_type': 'infoclimat', 'location_name': 'castanet-tolosan', 'lead_time_hours': -51.923611111111114, 'polled_at': '2026-11-12T07:40:25Z', 'kind': 'observation', 'forecast_time': '2026-11-10T03:45:00Z', 'temperature_c': 15, 'feels_like_c': None, 'humidity_pct': 92, 'dew_point_c': 13.7, 'pressure_hpa': 1010.4, 'wind_speed_kmh': 0.8, 'wind_gust_kmh': 4.8, 'wind_direction_deg': 158, 'cloud_cover_pct': None, 'precipitation_probability_pct': None, 'snowfall_cm': None, 'visibility_km': None, 'condition': 'unknown', 'condition_text': ''}
    limit = 500
    offset = 0
    all_data = []
    dt = datetime.now(UTC) if forecast_time is None else forecast_time
    now = dt.strftime("%Y-%m-%dT%H:%M:%SZ")
    while True:
        res = requests.get(
            "http://localhost:8777/api/v1/readings",
            auth=("test", "test"),
            params={
                "kind": kind,
                "forecast_timedelta": forecast_timedelta,
                "forecast_time": now,
                "limit": limit,
                "offset": offset,
            },
        )
        page = res.json()
        if not page:
            break
        all_data.extend(page)
        if len(page) < limit:
            break
        offset += limit

    df = pd.DataFrame(all_data)
    df["forecast_time"] = pd.to_datetime(df["forecast_time"], utc=True)

    return df


df_all = get_by_kind(kind="forecast", forecast_timedelta=90)
observations = get_by_kind(kind="observation", forecast_timedelta=90)
latest_idx = observations.groupby("location_name")["forecast_time"].idxmax()
stations_ft = [
    (row["location_name"], row["forecast_time"].to_pydatetime())
    for _, row in observations.loc[latest_idx][
        ["location_name", "forecast_time"]
    ].iterrows()
]


def analyse_station(
    station_name: str, forecast_time: datetime, forecast_timedelta: int = 90
):
    print(
        f"Analyse pour {station_name} à {forecast_time} avec un délai de prévision de {forecast_timedelta} minutes"
    )
    ft_min = forecast_time - timedelta(minutes=forecast_timedelta)
    ft_max = forecast_time + timedelta(minutes=forecast_timedelta)
    mask = (
        (df_all["location_name"] == station_name)
        & (df_all["forecast_time"] >= ft_min)
        & (df_all["forecast_time"] < ft_max)
    )
    return df_all[mask].copy()


station_name, station_ft = stations_ft[0]
df = analyse_station(
    station_name=station_name, forecast_time=station_ft, forecast_timedelta=90
)

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

obs_df = observations[observations["location_name"] == station_name].sort_values(
    "lead_time_hours"
)

fig = go.Figure()
marker_idx = 0
for provider, group in df.groupby("provider"):
    group = group.sort_values("lead_time_hours")
    marker_symbol = MARKERS[marker_idx % len(MARKERS)]
    marker_idx += 1
    fig.add_trace(
        go.Scatter(
            x=group["lead_time_hours"],
            y=group["temperature_c"],
            mode="lines+markers",
            name=provider,
            line=dict(dash="solid"),
            marker=dict(symbol=marker_symbol),
        )
    )

for provider, group in obs_df.groupby("provider"):
    for _, row in group.iterrows():
        fig.add_trace(
            go.Scatter(
                x=[x_min, x_max],
                y=[row["temperature_c"], row["temperature_c"]],
                mode="lines",
                name=f"{provider} (obs)",
                line={"dash": "dot"},
            )
        )
zero_utc = station_ft.strftime("%Y-%m-%d %H:%M UTC")
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
