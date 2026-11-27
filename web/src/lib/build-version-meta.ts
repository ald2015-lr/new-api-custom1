/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
/**
 * Name of the `<meta>` tag in index.html that carries the build version
 * (`VITE_REACT_APP_VERSION`). rsbuild.config.ts writes it and
 * lib/build-metadata.ts reads it.
 *
 * The version is kept out of the JavaScript on purpose: index.html is served
 * with `Cache-Control: no-cache`, while every script file is named after its
 * content. A version string inside the entry script gave the entry a new
 * name, and every visitor a full re-download, on each release.
 */
export const BUILD_VERSION_META_NAME = 'new-api-version'
