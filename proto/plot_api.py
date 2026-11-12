from datetime import UTC, datetime
from typing import Any

import pandas as pd
import plotly.graph_objects as go
import requests


def paginated_records(
    url: str, params: dict[str, Any], limit: int = 500, **kwargs
) -> list[dict[str, Any]]:
    offset = 0
    all_data: list[dict[str, Any]] = []
    while True:
        res = requests.get(
            url, params={**params, "limit": limit, "offset": offset}, **kwargs
        )
        page = res.json()
        if not page:
            break
        all_data.extend(page)
        if len(page) < limit:
            break
        offset += limit
    return all_data


def get_by_kind(
    kind: str,
    location: str,
    forecast_time: None | datetime = None,
):
    dt = datetime.now(UTC) if forecast_time is None else forecast_time
    sdt = dt.strftime("%Y-%m-%dT%H:%M:%SZ")
    all_data = paginated_records(
        url="http://localhost:8777/api/v1/readings",
        auth=("test", "test"),
        params={
            "kind": kind,
            "forecast_time": sdt,
            "location": location,
        },
    )

    df = pd.DataFrame(all_data)
    df["forecast_time"] = pd.to_datetime(df["forecast_time"], utc=True)

    return df.sort_values("lead_time_hours")


def get_latest_observations():
    all_data = paginated_records(
        url="http://localhost:8777/api/v1/locations",
        auth=("test", "test"),
        params={
            "latest": "true",
        },
    )
    return all_data


stations_ft = get_latest_observations()

station_name, station_ft = stations_ft[0]["name"], datetime.fromisoformat(
    stations_ft[0]["latest_forecast_time"]
)
df = get_by_kind(
    kind="forecast",
    location=station_name,
    forecast_time=station_ft,
)
observations = get_by_kind(
    kind="observation",
    location=station_name,
    forecast_time=station_ft,
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

# df.to_csv("df.csv", index=False)
# observations.to_csv("obs_df.csv", index=False)
# exit(0)

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

for provider, group in observations.groupby("provider"):
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
