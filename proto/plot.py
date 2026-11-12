from datetime import datetime, timedelta
import sqlite3

import pandas as pd
import plotly.graph_objects as go

conn = sqlite3.connect("meteocomp.db")
conn.row_factory = sqlite3.Row
cursor = conn.cursor()
cursor.execute("""
    SELECT *
    FROM (
        SELECT *,
               ROW_NUMBER() OVER (
                   PARTITION BY location_name
                   ORDER BY forecast_time DESC
               ) AS rn
        FROM readings
        WHERE kind = 'observation'
    )
    WHERE rn = 1
""")
result = [dict(row) for row in cursor.fetchall()]
stations_ft = [
    (r["location_name"], datetime.fromisoformat(r["forecast_time"])) for r in result
]


def analyse_station(station_name: str, forecast_time: datetime):
    print(station_name)
    ft_min = (forecast_time - timedelta(minutes=30)).isoformat()
    ft_max = (forecast_time + timedelta(minutes=30)).isoformat()
    df = pd.read_sql(
        f"SELECT * FROM readings WHERE location_name='{station_name}' AND forecast_time >= '{ft_min}' AND forecast_time < '{ft_max}'",
        conn,
    )
    return df


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
