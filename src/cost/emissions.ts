// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

/**
 * Electricity cost and CO₂ emissions for local inference.
 *
 * Grid carbon intensity: CO2.js by The Green Web Foundation
 *   https://github.com/thegreenwebfoundation/co2.js
 *   Data: Electricity Maps average intensity (gCO₂eq/kWh, 222 regions).
 *
 * Electricity prices: IEA World Energy Prices 2024, commercial/industrial tariffs
 *   (approximate; actual cost varies by tariff and sub-region).
 *
 * CO2.js uses ISO 3166-1 alpha-3 codes (e.g. "USA", "GBR").
 * This module accepts either alpha-2 ("US") or alpha-3 ("USA") and normalises.
 */

import { averageIntensity } from "@tgwf/co2";

// alpha-2 → alpha-3 for the jurisdictions where law firms are likely to operate.
const A2_TO_A3: Record<string, string> = {
  AU: "AUS", AT: "AUT", BE: "BEL", BR: "BRA", CA: "CAN",
  CN: "CHN", DE: "DEU", DK: "DNK", ES: "ESP", FI: "FIN",
  FR: "FRA", GB: "GBR", HK: "HKG", IN: "IND", IE: "IRL",
  IT: "ITA", JP: "JPN", KR: "KOR", NL: "NLD", NO: "NOR",
  NZ: "NZL", PL: "POL", PT: "PRT", SG: "SGP", SE: "SWE",
  TW: "TWN", US: "USA", ZA: "ZAF",
};

// Average commercial electricity prices in USD/kWh — IEA 2024 estimates.
const ELEC_PRICE: Record<string, number> = {
  AUS: 0.20, AUT: 0.24, BEL: 0.30, BRA: 0.12, CAN: 0.09,
  CHN: 0.08, DEU: 0.38, DNK: 0.35, ESP: 0.26, FIN: 0.20,
  FRA: 0.22, GBR: 0.35, HKG: 0.15, IND: 0.08, IRL: 0.30,
  ITA: 0.28, JPN: 0.22, KOR: 0.12, NLD: 0.32, NOR: 0.12,
  NZL: 0.18, POL: 0.20, PRT: 0.24, SGP: 0.18, SWE: 0.10,
  TWN: 0.09, USA: 0.16, ZAF: 0.12,
};

const INTENSITY_DATA = (averageIntensity as { data: Record<string, number> }).data;
const WORLD_AVG_INTENSITY = 475;  // gCO₂/kWh — Electricity Maps world average
const WORLD_AVG_PRICE     = 0.16; // USD/kWh  — US average as safe fallback

function toAlpha3(code: string): string {
  const u = code.toUpperCase();
  return u.length === 2 ? (A2_TO_A3[u] ?? u) : u;
}

export interface EmissionsResult {
  co2Grams: number;
  electricityCostUsd: number;
  intensityGPerKwh: number;
  priceUsdPerKwh: number;
  countryCode: string;  // resolved alpha-3
}

export function calcEmissions(wattHours: number, countryCode?: string): EmissionsResult {
  const alpha3     = countryCode ? toAlpha3(countryCode) : "WORLD";
  const intensity  = INTENSITY_DATA[alpha3] ?? WORLD_AVG_INTENSITY;
  const price      = ELEC_PRICE[alpha3] ?? WORLD_AVG_PRICE;
  const kWh        = wattHours / 1000;
  return {
    co2Grams:           Math.round(kWh * intensity * 10) / 10,
    electricityCostUsd: Math.round(kWh * price * 100000) / 100000,
    intensityGPerKwh:   intensity,
    priceUsdPerKwh:     price,
    countryCode:        alpha3,
  };
}
