import sqlite3

import pandas as pd
import plotly.graph_objects as go

conn = sqlite3.connect("meteocomp.db")
df = pd.read_sql("SELECT * FROM readings WHERE location_name='castanet-tolosan' AND polled_at>='2026-11-09T12:50:00' AND forecast_time >= strftime('%Y-%m-%dT%H:00:00', 'now') AND forecast_time <  strftime('%Y-%m-%dT%H:00:00', 'now', '+1 hour');", conn)

fig = go.Figure()
for provider, group in df.groupby('provider'):
    group = group.sort_values('lead_time_hours')
    dotted = (group['kind'] == 'observation').all()
    fig.add_trace(go.Scatter(
        x=group['polled_at'],
        y=group['temperature_c'],
        mode='lines+markers',
        name=provider,
        line=dict(dash='dot' if dotted else 'solid'),
    ))
fig.update_layout(xaxis_title='lead_time_hours', yaxis_title='temperature_c')
fig.update_xaxes(showgrid=True)
fig.update_yaxes(showgrid=True)
fig.show()
