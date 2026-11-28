export type WeatherLanguage = "en" | "zh";

export interface WeatherLocation {
  label: string;
  latitude: number;
  longitude: number;
}

export interface WeatherView {
  enabled: boolean;
  location?: string;
  estimated?: boolean;
  forecast?: {
    endsAt: string;
    condition: string;
    temperatureMin: number;
    temperatureMax: number;
  };
}

/** Load private weather or city lookup JSON without sliding the session window. */
export async function getWeatherJSON<T>(
  path: string,
  query: URLSearchParams,
  signal: AbortSignal,
): Promise<T> {
  const response = await fetch(`${path}?${query}`, {
    signal,
    cache: "no-store",
    headers: {
      "X-Session-Touch": "skip",
    },
  });

  if (!response.ok) {
    throw new Error("Weather request failed");
  }

  return response.json();
}
