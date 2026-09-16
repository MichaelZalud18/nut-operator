/*
Copyright 2026 Michael Zalud.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package resourcevalidation owns pure static resource checks shared by admission
// and reconciliation. It neither defaults nor mutates objects and performs no
// cluster reads. Admission adapts field errors to API errors; controllers adapt
// the first error and its optional origin to conditions. These checks do not
// replace fresh authorization, target resolution, or publication-time safety gates.
package resourcevalidation
